//go:build enterprise

// Copyright (c) 2026 Jonas Kaninda. All rights reserved.
//
// This file is part of Miabi Enterprise Edition.
//
// This file is licensed under the Miabi Enterprise License, not the GNU Affero
// General Public License. You may use it only with a valid Miabi Enterprise
// subscription and under the terms in internal/enterprise/LICENSE.md.
//
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

package enterprise

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/enterprise/license"
	"github.com/miabi-io/miabi/internal/enterprise/scim"
	eldap "github.com/miabi-io/miabi/internal/enterprise/sso/ldap"
	"github.com/miabi-io/miabi/internal/enterprise/sso/saml"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// Set at link time via -ldflags "-X github.com/miabi-io/miabi/internal/enterprise.embeddedKeyring=<base64>".
var embeddedKeyring string

// New falls back to MIABI_LICENSE_FILE; an empty instanceURL disables the URL check, install_id always applies.
func New(db *gorm.DB, licenseFile string, instanceURL string, installID string) EE {
	e := &impl{db: db, instanceURL: instanceURL, installID: installID}
	if ring, err := license.ParseKeyring(embeddedKeyring); err != nil || ring.Product != license.Product {
		logger.Warn("enterprise: no license keyring configured; licenses cannot be verified")
	} else {
		e.ring = &ring
	}
	if err := e.load(); err != nil {
		logger.Warn("enterprise: failed to load installed license", "error", err)
	}
	if e.claims == nil && licenseFile != "" {
		if err := e.installFromFile(licenseFile); err != nil {
			logger.Warn("enterprise: failed to install license from file", "file", licenseFile, "error", err)
		}
	}
	if e.claims != nil {
		logger.Info("enterprise: license active", "edition", e.claims.Edition, "customer", e.claims.Customer)
	}
	return e
}

type impl struct {
	db           *gorm.DB
	ring         *license.Keyring // nil ⇒ this build cannot verify licenses
	instanceURL  string           // this deployment's public URL (for the URL binding)
	installID    string           // this deployment's stable Install ID (for the install-id binding)
	mu           sync.RWMutex
	claims       *license.Claims   // nil ⇒ community
	bindMismatch bool              // claims valid but bound to a different instance ⇒ deny
	bindReason   string            // which binding failed (BindingError*), for display
	unknownKID   string            // the stored license is signed by this key, which the build does not trust
	saml         SAMLProvider      // nil until InitSSO + sso_saml entitled
	scim         SCIMProvider      // nil until InitSSO
	ldap         LDAPAuthenticator // nil until InitSSO
}

// Bindings are conjunctive; install_id is always enforced, url matches the instance URL or the request host.
func (e *impl) bindingResult(c *license.Claims, requestHost string) (ok bool, reason string) {
	if c == nil {
		return true, ""
	}
	if id := strings.TrimSpace(c.InstallID); id != "" {
		if !strings.EqualFold(id, strings.TrimSpace(e.installID)) {
			return false, BindingErrorInstallID
		}
	}
	if u := strings.TrimSpace(c.URL); u != "" {
		if !urlAllowed(u, e.instanceURL, requestHost) {
			return false, BindingErrorURL
		}
	}
	return true, ""
}

// Fails open when no candidate host is known, so a legitimate install is never bricked.
func urlAllowed(licenseURL string, candidates ...string) bool {
	want := normalizeHost(licenseURL)
	if want == "" {
		return true
	}
	known := false
	for _, cand := range candidates {
		h := normalizeHost(cand)
		if h == "" {
			continue
		}
		known = true
		if h == want {
			return true
		}
	}
	return !known // unknown host → fail open; known but unmatched → deny
}

func normalizeHost(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return s
}

// The handler re-checks the entitlement per request, so a later degrade disables it without a restart.
func (e *impl) InitSSO(deps SSODeps) {
	db, _ := deps.DB.(*gorm.DB)
	if db == nil {
		return
	}
	sp, err := saml.New(saml.Deps{
		DB:        db,
		BaseURL:   deps.BaseURL,
		SPKeyPEM:  deps.SPKeyPEM,
		SPCertPEM: deps.SPCertPEM,
		Login: func(ctx context.Context, email, name, provider string) (string, error) {
			return deps.Login(ctx, SSOIdentity{Email: email, Name: name, Provider: provider})
		},
		Entitled: func() bool { return e.Has(FlagSSOSAML) },
	})
	if err != nil {
		logger.Warn("enterprise: SAML init failed", "error", err)
	}
	scimProvider := scim.New(scim.Deps{DB: db, Entitled: func() bool { return e.Has(FlagSCIM) }})
	lp, lerr := eldap.New(eldap.Deps{
		DB:       db,
		Decrypt:  deps.Decrypt,
		Entitled: func() bool { return e.Has(FlagSSOLDAP) },
	})
	if lerr != nil {
		logger.Warn("enterprise: LDAP init failed", "error", lerr)
	}
	e.mu.Lock()
	if err == nil {
		e.saml = sp
	}
	e.scim = scimProvider
	if lerr == nil {
		e.ldap = &ldapAdapter{p: lp}
	}
	e.mu.Unlock()
}

// The provider uses its own types to avoid importing this package.
type ldapAdapter struct{ p *eldap.Provider }

func (a *ldapAdapter) Authenticate(ctx context.Context, username, password string) (LDAPIdentity, error) {
	id, err := a.p.Authenticate(ctx, username, password)
	if errors.Is(err, eldap.ErrNoMatch) {
		return LDAPIdentity{}, ErrLDAPNoMatch
	}
	if err != nil {
		return LDAPIdentity{}, err
	}
	return LDAPIdentity{
		Email: id.Email, Name: id.Name, Username: id.Username,
		Groups: id.Groups, Provider: id.Provider,
	}, nil
}

func (a *ldapAdapter) TestConnection(ctx context.Context, configID uint) (LDAPTestResult, error) {
	r, err := a.p.TestConnection(ctx, configID)
	if err != nil {
		return LDAPTestResult{}, err
	}
	return LDAPTestResult{OK: r.OK, Message: r.Message, Error: r.Error}, nil
}

func (e *impl) SAML() SAMLProvider {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.saml
}

func (e *impl) SCIM() SCIMProvider {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.scim
}

func (e *impl) LDAP() LDAPAuthenticator {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ldap
}

// The signed token is authoritative over cached columns; an untrusted signing key is reported, not dropped.
func (e *impl) load() error {
	var row models.License
	if err := e.db.Order("id DESC").First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if e.ring == nil {
		return nil
	}
	c, err := license.Verify(*e.ring, row.Token)
	if errors.Is(err, license.ErrUnknownKey) {
		kid := license.KeyID(row.Token)
		logger.Warn("enterprise: stored license is signed by a key this build does not trust; upgrade Miabi", "kid", kid)
		e.mu.Lock()
		e.unknownKID = kid
		e.mu.Unlock()
		return nil
	}
	if err != nil {
		logger.Warn("enterprise: stored license failed verification; reverting to community", "error", err)
		return nil
	}
	ok, reason := e.bindingResult(&c, "")
	e.mu.Lock()
	e.claims = &c
	e.bindMismatch = !ok
	e.bindReason = reason
	e.mu.Unlock()
	if !ok {
		logger.Warn("enterprise: license bound to a different deployment; features disabled",
			"reason", reason, "licensed_install_id", c.InstallID, "instance_install_id", e.installID,
			"licensed_url", c.URL, "instance_url", e.instanceURL)
	}
	return nil
}

func (e *impl) installFromFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = e.Install(context.Background(), string(b), "") // file/IaC install: no request host
	return err
}

func (e *impl) snapshot() license.Snapshot {
	e.mu.RLock()
	c := e.claims
	e.mu.RUnlock()
	if c == nil {
		return license.Snapshot{State: license.StateNone, Edition: EditionCommunity}
	}
	return license.Evaluate(*c, time.Now())
}

func (e *impl) mismatched() (bool, string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.bindMismatch, e.bindReason
}

func (e *impl) Entitlements() Entitlements {
	s := e.snapshot()
	ent := Entitlements{
		Edition:   s.Edition,
		Tier:      s.Tier,
		InstallID: s.InstallID,
		URL:       s.URL,
		State:     string(s.State),
		Customer:  s.Customer,
		LicenseID: s.LicenseID,
		Flags:     s.Flags,
		Limits:    s.Limits,
	}
	if ent.Edition == "" {
		ent.Edition = EditionCommunity
	}
	if s.State != license.StateNone {
		na, ge := s.NotAfter, s.GraceEnds
		ent.NotAfter, ent.GraceEnds = &na, &ge
	}
	// Keep edition/tier/install_id/url for display but drop flags.
	e.mu.RLock()
	unknownKID := e.unknownKID
	e.mu.RUnlock()
	if unknownKID != "" {
		ent.State = StateUnknownSigningKey
		ent.SigningKey = unknownKID
	}
	if mm, reason := e.mismatched(); mm {
		ent.State = StateBindingMismatch
		ent.BindingError = reason
		ent.Flags = map[string]bool{}
		ent.Limits = map[string]int{}
	}
	if ent.Flags == nil {
		ent.Flags = map[string]bool{}
	}
	if ent.Limits == nil {
		ent.Limits = map[string]int{}
	}
	return ent
}

// Has stays true when degraded, which keeps features running.
func (e *impl) Has(flag string) bool {
	if mm, _ := e.mismatched(); mm {
		return false
	}
	s := e.snapshot()
	return s.State != license.StateNone && s.Flags[flag]
}

// Configuration of a paid feature is frozen once past grace.
func (e *impl) Mutable(flag string) bool {
	if mm, _ := e.mismatched(); mm {
		return false
	}
	s := e.snapshot()
	return (s.State == license.StateValid || s.State == license.StateGrace) && s.Flags[flag]
}

func (e *impl) Require(flag string) error {
	if mm, _ := e.mismatched(); mm {
		return ErrLicenseBindingMismatch
	}
	s := e.snapshot()
	if s.State == license.StateNone {
		return ErrLicenseRequired
	}
	if s.Flags[flag] {
		return nil
	}
	return ErrEntitlementDenied
}

func (e *impl) RequireMutable(flag string) error {
	if err := e.Require(flag); err != nil {
		return err
	}
	if !e.Mutable(flag) {
		return ErrLicenseExpired
	}
	return nil
}

func (e *impl) Install(_ context.Context, token string, requestHost string) (Entitlements, error) {
	if e.ring == nil {
		return Entitlements{}, errors.New("this build has no license keyring, so it cannot verify licenses")
	}
	c, err := license.Verify(*e.ring, strings.TrimSpace(token))
	if errors.Is(err, license.ErrUnknownKey) {
		return Entitlements{}, ErrLicenseUnknownKey
	}
	if err != nil {
		return Entitlements{}, err
	}
	// Matched against the instance URL and the request host, so another deployment's license can't be installed.
	if ok, _ := e.bindingResult(&c, requestHost); !ok {
		return Entitlements{}, ErrLicenseBindingMismatch
	}
	row := models.License{
		LicenseID: c.LicenseID, Edition: c.Edition, Token: strings.TrimSpace(token),
		Customer: c.Customer, NotBefore: c.NotBefore, NotAfter: c.NotAfter, GraceDays: c.GraceDays,
	}
	err = e.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&models.License{}).Error; err != nil {
			return err
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return Entitlements{}, err
	}
	// Ongoing enforcement has no request host, so a mismatching configured URL surfaces now.
	ok, reason := e.bindingResult(&c, "")
	e.mu.Lock()
	e.claims = &c
	e.bindMismatch = !ok
	e.bindReason = reason
	e.unknownKID = ""
	e.mu.Unlock()
	return e.Entitlements(), nil
}

func (e *impl) Remove(_ context.Context) error {
	if err := e.db.Where("1 = 1").Delete(&models.License{}).Error; err != nil {
		return err
	}
	e.mu.Lock()
	e.claims = nil
	e.bindMismatch = false
	e.bindReason = ""
	e.unknownKID = ""
	e.mu.Unlock()
	return nil
}
