//go:build enterprise

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

// Package ldap implements LDAP / Active Directory authentication on top of go-ldap/ldap/v3,
// compiled only into the Enterprise build. It holds no core auth code — Authenticate returns a
// plain identity the core turns into a session, and the core owns provisioning and group mapping.
package ldap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	ldapv3 "github.com/go-ldap/ldap/v3"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// ErrNoMatch mirrors enterprise.ErrLDAPNoMatch (kept package-local to avoid an
// import cycle back into the enterprise package). The enterprise wiring maps a
// returned ErrNoMatch onto enterprise.ErrLDAPNoMatch.
var ErrNoMatch = errors.New("ldap: no matching configuration")

// ErrAccountDisabled is returned when a directory entry maps to a disabled state
// (e.g. AD userAccountControl). Currently unused by the bind path but reserved.
var ErrAccountDisabled = errors.New("ldap: account disabled")

// Identity is the resolved directory user (mirrors enterprise.LDAPIdentity).
type Identity struct {
	Email    string
	Name     string
	Username string
	Groups   []string
	Provider string
}

// Deps are the core dependencies, passed as plain values/closures so this
// package imports no core services.
type Deps struct {
	DB *gorm.DB
	// Decrypt reverses the core crypto service to read the bind password.
	Decrypt func(stored string) (string, error)
	// Entitled re-checks the sso_ldap license per request so a degrade disables
	// LDAP without a restart.
	Entitled func() bool
}

// Provider binds credentials against the configured directories.
type Provider struct {
	db       *gorm.DB
	decrypt  func(string) (string, error)
	entitled func() bool
}

// New builds the provider.
func New(d Deps) (*Provider, error) {
	if d.DB == nil {
		return nil, errors.New("ldap: DB is required")
	}
	decrypt := d.Decrypt
	if decrypt == nil {
		decrypt = func(s string) (string, error) { return s, nil }
	}
	entitled := d.Entitled
	if entitled == nil {
		entitled = func() bool { return true }
	}
	return &Provider{db: d.DB, decrypt: decrypt, entitled: entitled}, nil
}

// Authenticate tries each enabled config in order and returns the first identity
// whose credentials bind. An empty password is rejected outright (an empty bind
// is an anonymous bind that most directories accept — a classic auth bypass).
func (p *Provider) Authenticate(ctx context.Context, username, password string) (Identity, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return Identity{}, ErrNoMatch
	}
	if p.entitled != nil && !p.entitled() {
		return Identity{}, ErrNoMatch
	}
	var cfgs []models.LDAPConfig
	if err := p.db.WithContext(ctx).Where("enabled = ?", true).Order("id ASC").Find(&cfgs).Error; err != nil {
		return Identity{}, fmt.Errorf("ldap: load configs: %w", err)
	}
	if len(cfgs) == 0 {
		return Identity{}, ErrNoMatch
	}
	var lastErr error
	for i := range cfgs {
		ident, matched, err := p.authOne(ctx, &cfgs[i], username, password)
		if err != nil {
			// Bind rejected (bad credentials) or a transient dial error. Remember it
			// but keep trying other configs; only surface if none match.
			lastErr = err
			logger.Warn("ldap: auth attempt failed", "config", cfgs[i].Name, "error", err)
			continue
		}
		if matched {
			return ident, nil
		}
	}
	if lastErr != nil {
		return Identity{}, lastErr
	}
	return Identity{}, ErrNoMatch
}

// authOne runs the search+bind against a single config. It returns matched=false
// when the user isn't found in this directory (so the caller tries the next),
// and an error when the user is found but the password bind fails.
func (p *Provider) authOne(ctx context.Context, cfg *models.LDAPConfig, username, password string) (Identity, bool, error) {
	conn, err := p.dial(ctx, cfg)
	if err != nil {
		return Identity{}, false, err
	}
	defer func() { _ = conn.Close() }()

	// Bind as the service account (or anonymously) to search for the user entry.
	if cfg.BindDN != "" {
		pw, derr := p.decrypt(cfg.BindPasswordEnc)
		if derr != nil {
			return Identity{}, false, fmt.Errorf("ldap: decrypt bind password: %w", derr)
		}
		if err := conn.Bind(cfg.BindDN, pw); err != nil {
			return Identity{}, false, fmt.Errorf("ldap: service bind failed: %w", err)
		}
	}

	entry, found, err := p.searchUser(conn, cfg, username)
	if err != nil {
		return Identity{}, false, err
	}
	if !found {
		return Identity{}, false, nil // try the next config
	}

	// Re-bind as the user to verify the password. A fresh connection avoids reusing
	// the service-account bind state.
	userConn, err := p.dial(ctx, cfg)
	if err != nil {
		return Identity{}, false, err
	}
	defer func() { _ = userConn.Close() }()
	if err := userConn.Bind(entry.DN, password); err != nil {
		return Identity{}, false, fmt.Errorf("ldap: user bind failed: %w", err)
	}

	ident := Identity{
		Provider: cfg.Name,
		Email:    strings.TrimSpace(entry.GetAttributeValue(attrOr(cfg.AttrEmail, "mail"))),
		Name:     strings.TrimSpace(entry.GetAttributeValue(attrOr(cfg.AttrName, "displayName"))),
		Username: strings.TrimSpace(entry.GetAttributeValue(attrOr(cfg.AttrUsername, "uid"))),
	}
	if ident.Username == "" {
		ident.Username = username
	}
	ident.Groups = p.resolveGroups(userConn, cfg, entry)
	return ident, true, nil
}

// searchUser finds the user entry by the configured filter with the identifier
// safely escaped (LDAP injection guard).
func (p *Provider) searchUser(conn *ldapv3.Conn, cfg *models.LDAPConfig, username string) (*ldapv3.Entry, bool, error) {
	filterTmpl := strings.TrimSpace(cfg.UserFilter)
	if filterTmpl == "" {
		filterTmpl = "(uid=%s)"
	}
	filter := strings.ReplaceAll(filterTmpl, "%s", ldapv3.EscapeFilter(username))
	attrs := dedupe([]string{"dn",
		attrOr(cfg.AttrEmail, "mail"), attrOr(cfg.AttrName, "displayName"),
		attrOr(cfg.AttrUsername, "uid"), attrOr(cfg.MemberAttr, "memberOf")})
	req := ldapv3.NewSearchRequest(
		cfg.UserBaseDN, ldapv3.ScopeWholeSubtree, ldapv3.NeverDerefAliases,
		2, timeoutSeconds(cfg), false, filter, attrs, nil,
	)
	res, err := conn.Search(req)
	if err != nil {
		return nil, false, fmt.Errorf("ldap: user search: %w", err)
	}
	if len(res.Entries) == 0 {
		return nil, false, nil
	}
	return res.Entries[0], true, nil
}

// resolveGroups reads the user's group DNs, preferring the memberOf attribute and falling back to
// a group search by member. AD nested groups expand via LDAP_MATCHING_RULE_IN_CHAIN when
// configured. Best-effort: a failure yields no groups rather than blocking login.
func (p *Provider) resolveGroups(conn *ldapv3.Conn, cfg *models.LDAPConfig, entry *ldapv3.Entry) []string {
	memberAttr := attrOr(cfg.MemberAttr, "memberOf")
	if groups := entry.GetAttributeValues(memberAttr); len(groups) > 0 && !cfg.NestedGroups {
		return groups
	}
	if cfg.GroupBaseDN == "" && cfg.GroupFilter == "" {
		return entry.GetAttributeValues(memberAttr)
	}
	filter := groupFilter(cfg, entry.DN)
	if filter == "" {
		return entry.GetAttributeValues(memberAttr)
	}
	req := ldapv3.NewSearchRequest(
		orStr(cfg.GroupBaseDN, cfg.UserBaseDN), ldapv3.ScopeWholeSubtree, ldapv3.NeverDerefAliases,
		0, timeoutSeconds(cfg), false, filter, []string{"dn"}, nil,
	)
	res, err := conn.Search(req)
	if err != nil {
		logger.Warn("ldap: group search failed", "config", cfg.Name, "error", err)
		return entry.GetAttributeValues(memberAttr)
	}
	out := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, e.DN)
	}
	return out
}

func groupFilter(cfg *models.LDAPConfig, userDN string) string {
	if tmpl := strings.TrimSpace(cfg.GroupFilter); tmpl != "" {
		return strings.ReplaceAll(tmpl, "%s", ldapv3.EscapeFilter(userDN))
	}
	esc := ldapv3.EscapeFilter(userDN)
	if cfg.NestedGroups {
		// AD nested-group expansion via LDAP_MATCHING_RULE_IN_CHAIN.
		return "(member:1.2.840.113556.1.4.1941:=" + esc + ")"
	}
	return "(member=" + esc + ")"
}

// dial opens a connection honoring the configured TLS mode + optional private CA.
func (p *Provider) dial(ctx context.Context, cfg *models.LDAPConfig) (*ldapv3.Conn, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return nil, errors.New("ldap: host is required")
	}
	port := cfg.Port
	if port == 0 {
		if cfg.TLSMode == models.LDAPTLSLDAPS {
			port = 636
		} else {
			port = 389
		}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	timeout := time.Duration(timeoutSeconds(cfg)) * time.Second
	dialer := &net.Dialer{Timeout: timeout}

	var conn *ldapv3.Conn
	var err error
	switch cfg.TLSMode {
	case models.LDAPTLSLDAPS:
		conn, err = ldapv3.DialURL("ldaps://"+addr,
			ldapv3.DialWithDialer(dialer), ldapv3.DialWithTLSConfig(p.tlsConfig(cfg, host)))
	default:
		conn, err = ldapv3.DialURL("ldap://"+addr, ldapv3.DialWithDialer(dialer))
	}
	if err != nil {
		return nil, fmt.Errorf("ldap: dial %s: %w", addr, err)
	}
	conn.SetTimeout(timeout)
	if cfg.TLSMode == models.LDAPTLSStartTLS {
		if err := conn.StartTLS(p.tlsConfig(cfg, host)); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("ldap: starttls: %w", err)
		}
	}
	return conn, nil
}

func (p *Provider) tlsConfig(cfg *models.LDAPConfig, host string) *tls.Config {
	t := &tls.Config{ServerName: host, InsecureSkipVerify: cfg.InsecureSkipTLS} //nolint:gosec // opt-in, warned in UI
	if pem := strings.TrimSpace(cfg.CACertPEM); pem != "" {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM([]byte(pem)) {
			t.RootCAs = pool
		}
	}
	return t
}

// Result is the outcome of TestConnection (mirrors enterprise.LDAPTestResult; a
// package-local type so this package never imports the enterprise package).
type Result struct {
	OK      bool
	Message string
	Error   string
}

// TestConnection dials + service-binds a config (by id) and reports the result.
// A failed dial/bind/search is OK=false (not a Go error); a Go error means the
// config wasn't found. The core admin handler wraps the result in the envelope.
func (p *Provider) TestConnection(ctx context.Context, configID uint) (Result, error) {
	var cfg models.LDAPConfig
	if err := p.db.WithContext(ctx).First(&cfg, configID).Error; err != nil {
		return Result{}, err
	}
	conn, err := p.dial(ctx, &cfg)
	if err != nil {
		return Result{OK: false, Error: err.Error()}, nil
	}
	defer func() { _ = conn.Close() }()
	if cfg.BindDN != "" {
		pw, derr := p.decrypt(cfg.BindPasswordEnc)
		if derr != nil {
			return Result{OK: false, Error: "decrypt bind password: " + derr.Error()}, nil
		}
		if err := conn.Bind(cfg.BindDN, pw); err != nil {
			return Result{OK: false, Error: "service bind failed: " + err.Error()}, nil
		}
	}
	// A sample search validates the base DN + filter without needing a real user.
	msg := "connection OK"
	if cfg.UserBaseDN != "" {
		req := ldapv3.NewSearchRequest(cfg.UserBaseDN, ldapv3.ScopeBaseObject,
			ldapv3.NeverDerefAliases, 1, timeoutSeconds(&cfg), false, "(objectClass=*)", []string{"dn"}, nil)
		if _, serr := conn.Search(req); serr != nil {
			return Result{OK: false, Error: "base DN search failed: " + serr.Error()}, nil
		}
		msg = "connection + base DN OK"
	}
	return Result{OK: true, Message: msg}, nil
}

func timeoutSeconds(cfg *models.LDAPConfig) int {
	if cfg.TimeoutSeconds > 0 {
		return cfg.TimeoutSeconds
	}
	return 10
}

func attrOr(v, def string) string {
	if s := strings.TrimSpace(v); s != "" {
		return s
	}
	return def
}

func orStr(v, def string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
