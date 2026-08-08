//go:build enterprise

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// embeddedPublicKey is the license-signing public key baked in at build time via -ldflags. When
// empty, the runtime-configured MIABI_LICENSE_PUBLIC_KEY is used instead — convenient for dev.
// Production builds bake the key in so it cannot be swapped to forge a license.
var embeddedPublicKey string

// New constructs the real EE implementation: it loads any installed license from the database,
// falling back to MIABI_LICENSE_FILE for air-gapped installs. A license bound to a different
// instance grants no features. An empty instanceURL disables the URL check; install_id always applies.
func New(db *gorm.DB, publicKeyB64 string, licenseFile string, instanceURL string, installID string) EE {
	pub := strings.TrimSpace(publicKeyB64)
	if pub == "" {
		pub = strings.TrimSpace(embeddedPublicKey)
	}
	e := &impl{db: db, pub: pub, instanceURL: instanceURL, installID: installID}
	if pub == "" {
		logger.Warn("enterprise: no license public key configured; licenses cannot be verified")
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
	pub          string
	instanceURL  string // this deployment's public URL (for the URL binding)
	installID    string // this deployment's stable Install ID (for the install-id binding)
	mu           sync.RWMutex
	claims       *license.Claims   // nil ⇒ community
	bindMismatch bool              // claims valid but bound to a different instance ⇒ deny
	bindReason   string            // which binding failed (BindingError*), for display
	saml         SAMLProvider      // nil until InitSSO + sso_saml entitled
	scim         SCIMProvider      // nil until InitSSO
	ldap         LDAPAuthenticator // nil until InitSSO
}

// bindingResult reports whether the license's bindings all match this deployment and, if not,
// which failed. Bindings are conjunctive. install_id is an exact match and always enforced (the
// strong, primary binding); url matches the configured instance URL or the request host.
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

// urlAllowed reports whether a license bound to licenseURL is allowed given the deployment's known
// host identities. An empty licenseURL is unlimited and empty candidates are ignored; if no
// candidate host is known the check fails open, so a legitimate install is never bricked.
func urlAllowed(licenseURL string, candidates ...string) bool {
	want := normalizeHost(licenseURL)
	if want == "" {
		return true // unlimited: any URL, any number of instances
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

// normalizeHost reduces a deployment URL to its lowercased hostname so the
// binding matches regardless of scheme, port, path, or a trailing slash.
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

// InitSSO constructs the SAML provider when the license entitles it. The handler
// re-checks the entitlement per request, so a later degrade disables it without
// a restart.
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

// ldapAdapter bridges the enterprise/sso/ldap provider (which uses its own types
// to avoid importing this package) to the enterprise.LDAPAuthenticator interface.
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

// load reads the active license row and re-verifies its signed token. A row that
// fails verification (tampered DB, rotated key) is ignored, falling back to
// community — the signed token is authoritative, not the cached columns.
func (e *impl) load() error {
	var row models.License
	if err := e.db.Order("id DESC").First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	c, err := license.Verify(e.pub, row.Token)
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

// mismatched reports whether the installed license is bound to a different
// instance than this one (so no features are granted), plus the failed binding.
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
	// A license bound to a different instance grants nothing: report the mismatch
	// state (keeping edition/tier/install_id/url for display) and drop flags.
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

// Has reports whether an entitled flag may be used at runtime: true in every
// non-community state (including degraded, which keeps features running). A
// license bound to a different instance grants nothing.
func (e *impl) Has(flag string) bool {
	if mm, _ := e.mismatched(); mm {
		return false
	}
	s := e.snapshot()
	return s.State != license.StateNone && s.Flags[flag]
}

// Mutable additionally requires the license not be degraded: configuration of a
// paid feature is frozen once past grace.
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

// Install verifies the token against the public key and persists it as the
// single active license, replacing any prior row.
func (e *impl) Install(_ context.Context, token string, requestHost string) (Entitlements, error) {
	if e.pub == "" {
		return Entitlements{}, errors.New("no license public key configured")
	}
	c, err := license.Verify(e.pub, strings.TrimSpace(token))
	if err != nil {
		return Entitlements{}, err
	}
	// Reject a license bound to a different deployment at install time. The URL binding is matched
	// against both the configured instance URL and the live request host, so you cannot install a
	// license for another deployment even when the instance URL is unset.
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
	// Ongoing enforcement has no request host (there is none at boot), so reflect
	// that rule now too — a license validated only against the request host but a
	// mismatching configured URL surfaces the misconfig.
	ok, reason := e.bindingResult(&c, "")
	e.mu.Lock()
	e.claims = &c
	e.bindMismatch = !ok
	e.bindReason = reason
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
	e.mu.Unlock()
	return nil
}
