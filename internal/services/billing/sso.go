// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package billing

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// ConsoleProviderDisplayName is the login-button label of the console's provider.
const ConsoleProviderDisplayName = "Miabi Console"

var (
	ErrSSONameTaken     = coded(http.StatusConflict, "SSO_NAME_TAKEN", "another provider is already named "+models.ConsoleOAuthProviderName+"; rename or delete it first")
	ErrSSOIssuer        = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "issuer must be an https address on the connected console's host")
	ErrSSOFields        = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "issuer, client_id and client_secret are required")
	ErrSSOUnavailable   = coded(http.StatusServiceUnavailable, "BILLING_SSO_UNAVAILABLE", "SSO providers are unavailable on this instance")
	ErrSSONotConfigured = coded(http.StatusNotFound, "BILLING_SSO_NOT_CONFIGURED", "no console-managed SSO provider")
)

// SSOProviderInput is PUT /billing/sso-provider: the console's OIDC client for this instance.
type SSOProviderInput struct {
	Issuer       string `json:"issuer" required:"true"`
	ClientID     string `json:"client_id" required:"true"`
	ClientSecret string `json:"client_secret" required:"true"`
	Scopes       string `json:"scopes"`
	AutoRegister *bool  `json:"auto_register"`
	// GroupsClaim and GroupPrefix turn on structured group sync ("<prefix>:<workspace>:<role>").
	GroupsClaim string `json:"groups_claim"`
	GroupPrefix string `json:"group_prefix"`
	// AdminGroups are groups granting the platform admin role (console staff).
	AdminGroups []string `json:"admin_groups"`
}

// SSOProvider is the managed provider as the Billing API reports it.
type SSOProvider struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Issuer      string `json:"issuer"`
	Enabled     bool   `json:"enabled"`
	CallbackURL string `json:"callback_url"`
	// GroupSync is "on", "off" (not requested) or "unlicensed" (the licence lacks sso_group_mapping).
	GroupSync string `json:"group_sync" enum:"on,off,unlicensed"`
	// Adopted reports that an existing provider was taken over rather than created.
	Adopted bool `json:"adopted"`
}

// SetSSO wires the provider store, the OIDC discovery and the API base the callback URL is built on.
func (s *Service) SetSSO(repo *repositories.OAuthProviderRepository, resolve func(ctx context.Context, p *models.OAuthProvider) error, apiBase string) {
	s.ssoRepo, s.ssoResolve, s.ssoAPIBase = repo, resolve, strings.TrimRight(apiBase, "/")
}

// UpsertSSOProvider creates or updates the console-managed provider, adopting a hand-configured
// provider with the same issuer so its users stay linked.
func (s *Service) UpsertSSOProvider(ctx context.Context, in SSOProviderInput) (*SSOProvider, error) {
	if s.ssoRepo == nil {
		return nil, ErrSSOUnavailable
	}
	in.Issuer = strings.TrimRight(strings.TrimSpace(in.Issuer), "/")
	in.ClientID, in.ClientSecret = strings.TrimSpace(in.ClientID), strings.TrimSpace(in.ClientSecret)
	if in.Issuer == "" || in.ClientID == "" || in.ClientSecret == "" {
		return nil, ErrSSOFields
	}
	conn, err := s.Connection()
	if err != nil {
		return nil, err
	}
	if !conn.Connected {
		return nil, ErrNotConnected
	}
	if !issuerOnConsole(in.Issuer, conn.ConsoleURL) {
		return nil, ErrSSOIssuer
	}

	p, adopted, err := s.consoleProvider(in.Issuer)
	if err != nil {
		return nil, err
	}
	enc, err := crypto.Encrypt(in.ClientSecret)
	if err != nil {
		return nil, err
	}
	if p == nil {
		p = &models.OAuthProvider{Enabled: true, AutoRegister: true}
	}
	p.Name, p.DisplayName, p.Type = models.ConsoleOAuthProviderName, ConsoleProviderDisplayName, models.OAuthProviderOIDC
	p.ManagedBy = models.ManagedByConsole
	p.Issuer, p.ClientID, p.ClientSecretEnc = in.Issuer, in.ClientID, enc
	// Re-discovered from the issuer on every push, so a console that moves its endpoints is followed.
	p.AuthURL, p.TokenURL, p.UserInfoURL = "", "", ""
	p.Scopes = strings.TrimSpace(in.Scopes)
	if p.Scopes == "" {
		p.Scopes = "openid email profile groups"
	}
	if in.AutoRegister != nil {
		p.AutoRegister = *in.AutoRegister
	}

	groupSync := "off"
	var mappings []models.OAuthGroupMapping
	if claim := strings.TrimSpace(in.GroupsClaim); claim != "" {
		if s.deps.EE != nil && s.deps.EE.Has(enterprise.FlagSSOGroupMapping) {
			groupSync = "on"
			p.GroupsClaim, p.GroupPrefix = claim, strings.ToLower(strings.TrimSpace(in.GroupPrefix))
			for _, g := range in.AdminGroups {
				if g = strings.TrimSpace(g); g != "" {
					mappings = append(mappings, models.OAuthGroupMapping{Group: g, SystemAdmin: true})
				}
			}
		} else {
			groupSync = "unlicensed"
			p.GroupsClaim, p.GroupPrefix = "", ""
		}
	} else {
		p.GroupsClaim, p.GroupPrefix = "", ""
	}

	if s.ssoResolve != nil {
		if err := s.ssoResolve(ctx, p); err != nil {
			return nil, coded(http.StatusBadGateway, "BILLING_SSO_DISCOVERY_FAILED", "OIDC discovery failed: "+err.Error())
		}
	}
	if p.ID == 0 {
		err = s.ssoRepo.Create(p)
	} else {
		err = s.ssoRepo.Update(p)
	}
	if err != nil {
		return nil, err
	}
	if err := s.ssoRepo.ReplaceMappings(p.ID, mappings); err != nil {
		return nil, err
	}
	return s.renderSSO(p, groupSync, adopted), nil
}

// consoleProvider finds the provider the console's push applies to: the one already named
// miabi-console, else a hand-configured one with the same issuer (adopted). A different provider
// holding the name is refused rather than overwritten.
func (s *Service) consoleProvider(issuer string) (*models.OAuthProvider, bool, error) {
	byName, err := s.ssoRepo.FindByName(models.ConsoleOAuthProviderName)
	switch {
	case err == nil:
		if byName.ConsoleManaged() || sameIssuer(byName.Issuer, issuer) {
			return byName, !byName.ConsoleManaged(), nil
		}
		return nil, false, ErrSSONameTaken
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, false, err
	}
	all, err := s.ssoRepo.FindAll()
	if err != nil {
		return nil, false, err
	}
	for i := range all {
		if sameIssuer(all[i].Issuer, issuer) {
			return &all[i], true, nil
		}
	}
	return nil, false, nil
}

// SSOProvider reports the console-managed provider.
func (s *Service) SSOProvider() (*SSOProvider, error) {
	if s.ssoRepo == nil {
		return nil, ErrSSOUnavailable
	}
	p, err := s.ssoRepo.FindByName(models.ConsoleOAuthProviderName)
	if err != nil || !p.ConsoleManaged() {
		return nil, ErrSSONotConfigured
	}
	sync := "off"
	if p.GroupsClaim != "" {
		sync = "on"
	}
	return s.renderSSO(p, sync, false), nil
}

// DeleteSSOProvider removes the console-managed provider. A hand-configured provider is never touched.
func (s *Service) DeleteSSOProvider() error {
	if s.ssoRepo == nil {
		return ErrSSOUnavailable
	}
	p, err := s.ssoRepo.FindByName(models.ConsoleOAuthProviderName)
	if err != nil || !p.ConsoleManaged() {
		return ErrSSONotConfigured
	}
	return s.ssoRepo.Delete(p.ID)
}

func (s *Service) renderSSO(p *models.OAuthProvider, groupSync string, adopted bool) *SSOProvider {
	return &SSOProvider{
		Name: p.Name, DisplayName: p.DisplayName, Issuer: p.Issuer, Enabled: p.Enabled,
		CallbackURL: s.ssoAPIBase + "/auth/oauth/" + p.Name + "/callback",
		GroupSync:   groupSync, Adopted: adopted,
	}
}

// wouldLockOut reports whether removing the console provider leaves the actor without a way in: an
// SSO-only account whose only enabled provider link is the console's.
func (s *Service) wouldLockOut(actorID uint) (bool, error) {
	if actorID == 0 || s.users == nil {
		return false, nil
	}
	u, err := s.users.FindByID(actorID)
	if err != nil || u.AuthSource != models.AuthSourceOAuth {
		return false, nil
	}
	var providerIDs []uint
	if err := s.db.Model(&models.UserIdentity{}).Where("user_id = ?", actorID).Pluck("provider_id", &providerIDs).Error; err != nil {
		return false, err
	}
	var links []models.OAuthProvider
	if len(providerIDs) > 0 {
		if err := s.db.Select("id", "name", "enabled", "managed_by").Where("id IN ?", providerIDs).Find(&links).Error; err != nil {
			return false, err
		}
	}
	viaConsole := false
	for _, l := range links {
		if l.Name == models.ConsoleOAuthProviderName && l.ManagedBy == models.ManagedByConsole {
			viaConsole = true
		} else if l.Enabled {
			return false, nil
		}
	}
	return viaConsole, nil
}

func issuerOnConsole(issuer, consoleURL string) bool {
	iu, err := url.Parse(issuer)
	if err != nil || iu.Host == "" || iu.User != nil {
		return false
	}
	if iu.Scheme != "https" && !validConsoleURL(issuer) {
		return false
	}
	return strings.EqualFold(iu.Host, mustHost(consoleURL))
}

func sameIssuer(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	return a != "" && norm(a) == norm(b)
}
