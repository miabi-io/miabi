// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/oauth"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// errSSOProviderLimit is returned when a Community install (no multi_sso
// entitlement) already has its single allowed provider. Maps to HTTP 403.
var errSSOProviderLimit = &ssoProviderLimitError{}

type ssoProviderLimitError struct{}

func (*ssoProviderLimitError) Error() string {
	return "Community Edition allows one SSO provider; an Enterprise license unlocks multiple"
}
func (*ssoProviderLimitError) Code() string { return "SSO_PROVIDER_LIMIT" }

// OAuthAdminHandler manages SSO provider configuration (super-admin only).
type OAuthAdminHandler struct {
	repo  *repositories.OAuthProviderRepository
	oauth *oauth.Service
	ee    enterprise.EE
	audit *audit.Logger
}

func NewOAuthAdminHandler(repo *repositories.OAuthProviderRepository, oauthSvc *oauth.Service, ee enterprise.EE, auditLog *audit.Logger) *OAuthAdminHandler {
	return &OAuthAdminHandler{repo: repo, oauth: oauthSvc, ee: ee, audit: auditLog}
}

// providerCountAllowed reports whether another provider may be created.
// Community is capped at one; multi_sso lifts the cap.
func providerCountAllowed(ee enterprise.EE, current int64) bool {
	if ee != nil && ee.Has(enterprise.FlagMultiSSO) {
		return true
	}
	return current < 1
}

// hiddenAllowed reports whether a provider may be hidden from the login page (an
// Enterprise capability). Community installs always list providers.
func hiddenAllowed(ee enterprise.EE) bool {
	return ee != nil && ee.Has(enterprise.FlagSSOHiddenProvider)
}

func (h *OAuthAdminHandler) canHide() bool { return hiddenAllowed(h.ee) }

type CreateOAuthProviderRequest struct {
	Body struct {
		DisplayName        string `json:"display_name" required:"true"`
		Name               string `json:"name"`
		Type               string `json:"type" required:"true" enum:"google,oidc"`
		ClientID           string `json:"client_id" required:"true"`
		ClientSecret       string `json:"client_secret" required:"true"`
		Issuer             string `json:"issuer"`
		AuthURL            string `json:"auth_url"`
		TokenURL           string `json:"token_url"`
		UserInfoURL        string `json:"userinfo_url"`
		Scopes             string `json:"scopes"`
		Enabled            *bool  `json:"enabled"`
		Hidden             *bool  `json:"hidden"`
		AutoRegister       *bool  `json:"auto_register"`
		AllowedDomains     string `json:"allowed_domains"`
		EmailClaim         string `json:"email_claim"`
		NameClaim          string `json:"name_claim"`
		UsernameClaim      string `json:"username_claim"`
		DefaultWorkspaceID *uint  `json:"default_workspace_id"`
		DefaultRole        string `json:"default_role"`
	} `json:"body"`
}

type UpdateOAuthProviderRequest struct {
	Body struct {
		// DisplayName edits the login-button label. The Name handle is immutable
		// here because it is part of the provider's OAuth callback URL.
		DisplayName        string  `json:"display_name"`
		ClientID           string  `json:"client_id"`
		ClientSecret       string  `json:"client_secret"` // blank keeps the stored secret
		Issuer             string  `json:"issuer"`
		AuthURL            string  `json:"auth_url"`
		TokenURL           string  `json:"token_url"`
		UserInfoURL        string  `json:"userinfo_url"`
		Scopes             string  `json:"scopes"`
		Enabled            *bool   `json:"enabled"`
		Hidden             *bool   `json:"hidden"`
		AutoRegister       *bool   `json:"auto_register"`
		AllowedDomains     *string `json:"allowed_domains"`
		EmailClaim         *string `json:"email_claim"`
		NameClaim          *string `json:"name_claim"`
		UsernameClaim      *string `json:"username_claim"`
		DefaultWorkspaceID *uint   `json:"default_workspace_id"`
		DefaultRole        *string `json:"default_role"`
	} `json:"body"`
}

// List returns all configured providers.
func (h *OAuthAdminHandler) List(c *okapi.Context) error {
	providers, err := h.repo.FindAll()
	if err != nil {
		return c.AbortInternalServerError("failed to list providers", err)
	}
	return ok(c, providers)
}

// Create configures a new provider, resolving its endpoints and encrypting the
// client secret at rest.
func (h *OAuthAdminHandler) Create(c *okapi.Context, req *CreateOAuthProviderRequest) error {
	// Single-provider quota: Community allows one provider; multi_sso lifts it.
	if n, err := h.repo.Count(); err == nil && !providerCountAllowed(h.ee, n) {
		return c.AbortWithError(403, errSSOProviderLimit)
	}
	// Hidden providers are an Enterprise capability; reject an explicit
	// opt-in without the entitlement (the field is otherwise forced false below).
	if boolOr(req.Body.Hidden, false) && !h.canHide() {
		return entitlementAbort(c, enterprise.ErrEntitlementDenied)
	}

	s := strings.TrimSpace(req.Body.Name)
	if s == "" {
		s = req.Body.DisplayName
	}
	s = slug.Make(s, "provider")
	if exists, _ := h.repo.ExistsByName(s); exists {
		return c.AbortWithError(409, errSlugTaken)
	}

	enc, err := crypto.Encrypt(req.Body.ClientSecret)
	if err != nil {
		return c.AbortInternalServerError("failed to encrypt secret", err)
	}

	p := &models.OAuthProvider{
		Name:               s,
		DisplayName:        strings.TrimSpace(req.Body.DisplayName),
		Type:               models.OAuthProviderType(req.Body.Type),
		ClientID:           strings.TrimSpace(req.Body.ClientID),
		ClientSecretEnc:    enc,
		Issuer:             strings.TrimSpace(req.Body.Issuer),
		AuthURL:            strings.TrimSpace(req.Body.AuthURL),
		TokenURL:           strings.TrimSpace(req.Body.TokenURL),
		UserInfoURL:        strings.TrimSpace(req.Body.UserInfoURL),
		Scopes:             strings.TrimSpace(req.Body.Scopes),
		Enabled:            boolOr(req.Body.Enabled, true),
		Hidden:             boolOr(req.Body.Hidden, false) && h.canHide(),
		AutoRegister:       boolOr(req.Body.AutoRegister, true),
		AllowedDomains:     strings.TrimSpace(req.Body.AllowedDomains),
		EmailClaim:         strings.TrimSpace(req.Body.EmailClaim),
		NameClaim:          strings.TrimSpace(req.Body.NameClaim),
		UsernameClaim:      strings.TrimSpace(req.Body.UsernameClaim),
		DefaultWorkspaceID: req.Body.DefaultWorkspaceID,
		DefaultRole:        models.WorkspaceRole(strings.TrimSpace(req.Body.DefaultRole)),
	}
	if err := h.oauth.ResolveEndpoints(c.Request().Context(), p); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if err := h.repo.Create(p); err != nil {
		return c.AbortInternalServerError("failed to create provider", err)
	}
	h.record(c, "admin.oauth.create", p.ID, map[string]any{"name": p.Name, "type": string(p.Type)})
	return created(c, p)
}

// Update edits a provider. A blank client_secret preserves the existing one.
func (h *OAuthAdminHandler) Update(c *okapi.Context, req *UpdateOAuthProviderRequest) error {
	p, err := h.provider(c)
	if err != nil {
		return c.AbortNotFound("provider not found")
	}
	if v := strings.TrimSpace(req.Body.DisplayName); v != "" {
		p.DisplayName = v
	}
	if v := strings.TrimSpace(req.Body.ClientID); v != "" {
		p.ClientID = v
	}
	if v := strings.TrimSpace(req.Body.ClientSecret); v != "" {
		enc, err := crypto.Encrypt(v)
		if err != nil {
			return c.AbortInternalServerError("failed to encrypt secret", err)
		}
		p.ClientSecretEnc = enc
	}
	p.Issuer = strings.TrimSpace(req.Body.Issuer)
	p.AuthURL = strings.TrimSpace(req.Body.AuthURL)
	p.TokenURL = strings.TrimSpace(req.Body.TokenURL)
	p.UserInfoURL = strings.TrimSpace(req.Body.UserInfoURL)
	if v := strings.TrimSpace(req.Body.Scopes); v != "" {
		p.Scopes = v
	}
	if req.Body.Enabled != nil {
		p.Enabled = *req.Body.Enabled
	}
	if req.Body.Hidden != nil {
		// Turning a provider hidden requires the entitlement; clearing it is always
		// allowed (additive, never traps a provider hidden after a downgrade).
		if *req.Body.Hidden && !h.canHide() {
			return entitlementAbort(c, enterprise.ErrEntitlementDenied)
		}
		p.Hidden = *req.Body.Hidden
	}
	if req.Body.AutoRegister != nil {
		p.AutoRegister = *req.Body.AutoRegister
	}
	if req.Body.AllowedDomains != nil {
		p.AllowedDomains = strings.TrimSpace(*req.Body.AllowedDomains)
	}
	if req.Body.EmailClaim != nil {
		p.EmailClaim = strings.TrimSpace(*req.Body.EmailClaim)
	}
	if req.Body.UsernameClaim != nil {
		p.UsernameClaim = strings.TrimSpace(*req.Body.UsernameClaim)
	}
	if req.Body.NameClaim != nil {
		p.NameClaim = strings.TrimSpace(*req.Body.NameClaim)
	}
	if req.Body.DefaultWorkspaceID != nil {
		// 0 clears the auto-join target; any other id sets it.
		if *req.Body.DefaultWorkspaceID == 0 {
			p.DefaultWorkspaceID = nil
		} else {
			p.DefaultWorkspaceID = req.Body.DefaultWorkspaceID
		}
	}
	if req.Body.DefaultRole != nil {
		p.DefaultRole = models.WorkspaceRole(strings.TrimSpace(*req.Body.DefaultRole))
	}
	if err := h.oauth.ResolveEndpoints(c.Request().Context(), p); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if err := h.repo.Update(p); err != nil {
		return c.AbortInternalServerError("failed to update provider", err)
	}
	h.record(c, "admin.oauth.update", p.ID, map[string]any{"name": p.Name})
	return ok(c, p)
}

// Delete removes a provider.
func (h *OAuthAdminHandler) Delete(c *okapi.Context) error {
	p, err := h.provider(c)
	if err != nil {
		return c.AbortNotFound("provider not found")
	}
	if err := h.repo.Delete(p.ID); err != nil {
		return c.AbortInternalServerError("failed to delete provider", err)
	}
	h.record(c, "admin.oauth.delete", p.ID, map[string]any{"name": p.Name})
	return message(c, "provider deleted")
}

func (h *OAuthAdminHandler) provider(c *okapi.Context) (*models.OAuthProvider, error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return nil, errInvalidID
	}
	return h.repo.FindByID(uint(id))
}

func (h *OAuthAdminHandler) record(c *okapi.Context, action string, id uint, meta map[string]any) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: action, TargetType: "oauth_provider",
		TargetID: strconv.Itoa(int(id)), IP: c.RealIP(), Metadata: meta,
	})
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
