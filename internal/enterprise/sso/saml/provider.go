//go:build enterprise

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

// Package saml implements a SAML 2.0 service provider on top of crewjam/saml, compiled only into
// the Enterprise build. It holds no core dependencies — it maps an authenticated assertion to a
// session via a Login callback supplied by the routes layer.
package saml

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// Deps are the core dependencies, passed as plain values/closures so this
// package never imports auth/session/user code.
type Deps struct {
	DB        *gorm.DB
	BaseURL   string
	SPKeyPEM  string
	SPCertPEM string
	// Login maps an authenticated identity to a session and returns the post-login
	// redirect URL (the SPA callback carrying the token).
	Login func(ctx context.Context, email, name, provider string) (string, error)
	// Entitled re-checks the sso_saml license per request so a degrade disables
	// SAML without a restart.
	Entitled func() bool
}

// Provider is the SAML service-provider handler set. Its Metadata/Login/ACS
// methods satisfy enterprise.SAMLProvider structurally.
type Provider struct {
	db       *gorm.DB
	baseURL  string
	key      *rsa.PrivateKey
	cert     *x509.Certificate
	login    func(ctx context.Context, email, name, provider string) (string, error)
	entitled func() bool
}

// New builds the provider, loading or generating the SP signing key pair.
func New(d Deps) (*Provider, error) {
	if d.DB == nil || d.Login == nil {
		return nil, errors.New("saml: DB and Login are required")
	}
	key, cert, err := loadOrGenerateKeyPair(d.SPKeyPEM, d.SPCertPEM)
	if err != nil {
		return nil, fmt.Errorf("saml: key pair: %w", err)
	}
	entitled := d.Entitled
	if entitled == nil {
		entitled = func() bool { return true }
	}
	return &Provider{
		db: d.DB, baseURL: strings.TrimRight(d.BaseURL, "/"),
		key: key, cert: cert, login: d.Login, entitled: entitled,
	}, nil
}

// serviceProvider builds a crewjam ServiceProvider for a stored config: the SP
// key/cert, the per-config ACS/metadata URLs, and the parsed IdP metadata.
func (p *Provider) serviceProvider(ctx context.Context, cfg *models.SAMLConfig) (*saml.ServiceProvider, error) {
	idp, err := p.idpMetadata(ctx, cfg)
	if err != nil {
		return nil, err
	}
	acs, _ := url.Parse(p.baseURL + "/auth/saml/" + cfg.Name + "/acs")
	meta, _ := url.Parse(p.baseURL + "/auth/saml/" + cfg.Name + "/metadata")
	entityID := cfg.SPEntityID
	if entityID == "" {
		entityID = meta.String()
	}
	return &saml.ServiceProvider{
		EntityID:    entityID,
		Key:         p.key,
		Certificate: p.cert,
		MetadataURL: *meta,
		AcsURL:      *acs,
		IDPMetadata: idp,
		// The IdP is a trusted, admin-configured enterprise directory; allow
		// IdP-initiated SSO. Assertion signatures are still fully validated.
		AllowIDPInitiated: true,
	}, nil
}

func (p *Provider) idpMetadata(ctx context.Context, cfg *models.SAMLConfig) (*saml.EntityDescriptor, error) {
	if xml := strings.TrimSpace(cfg.IDPMetadataXML); xml != "" {
		return samlsp.ParseMetadata([]byte(xml))
	}
	if u := strings.TrimSpace(cfg.IDPMetadataURL); u != "" {
		parsed, err := url.Parse(u)
		if err != nil {
			return nil, err
		}
		return samlsp.FetchMetadata(ctx, http.DefaultClient, *parsed)
	}
	return nil, errors.New("saml: config has neither metadata XML nor URL")
}

func (p *Provider) config(slug string) (*models.SAMLConfig, error) {
	var cfg models.SAMLConfig
	if err := p.db.Where("slug = ? AND enabled = ?", slug, true).First(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Metadata serves the SP metadata XML for the {slug} config.
func (p *Provider) Metadata(c *okapi.Context) error {
	if !p.entitled() {
		return c.AbortWithError(402, errLicense)
	}
	cfg, err := p.config(c.Param("slug"))
	if err != nil {
		return c.AbortNotFound("saml config not found")
	}
	sp, err := p.serviceProvider(c.Request().Context(), cfg)
	if err != nil {
		return c.AbortInternalServerError("saml metadata", err)
	}
	xml, err := xmlMarshal(sp.Metadata())
	if err != nil {
		return c.AbortInternalServerError("saml metadata", err)
	}
	return c.Data(http.StatusOK, "application/samlmetadata+xml", xml)
}

// Login initiates SP-initiated SSO: redirect the browser to the IdP.
func (p *Provider) Login(c *okapi.Context) error {
	if !p.entitled() {
		return c.AbortWithError(402, errLicense)
	}
	cfg, err := p.config(c.Param("slug"))
	if err != nil {
		return c.AbortNotFound("saml config not found")
	}
	sp, err := p.serviceProvider(c.Request().Context(), cfg)
	if err != nil {
		return c.AbortInternalServerError("saml login", err)
	}
	redirect, err := sp.MakeRedirectAuthenticationRequest("" /* relayState */)
	if err != nil {
		return c.AbortInternalServerError("saml authn request", err)
	}
	c.Redirect(http.StatusFound, redirect.String())
	return nil
}

// ACS is the assertion consumer service: validate the IdP's response, extract
// the identity, and hand it to the core Login callback for a session.
func (p *Provider) ACS(c *okapi.Context) error {
	if !p.entitled() {
		return c.AbortWithError(402, errLicense)
	}
	cfg, err := p.config(c.Param("slug"))
	if err != nil {
		return c.AbortNotFound("saml config not found")
	}
	sp, err := p.serviceProvider(c.Request().Context(), cfg)
	if err != nil {
		return c.AbortInternalServerError("saml acs", err)
	}
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return c.AbortBadRequest("invalid saml response")
	}
	assertion, err := sp.ParseResponse(req, nil)
	if err != nil {
		logger.Warn("saml: assertion rejected", "slug", cfg.Name, "error", err)
		return c.AbortUnauthorized("saml assertion rejected")
	}
	email, name := identityFromAssertion(assertion, cfg)
	if email == "" {
		return c.AbortUnauthorized("saml assertion has no email")
	}
	redirect, err := p.login(req.Context(), email, name, cfg.Name)
	if err != nil {
		logger.Warn("saml: login failed", "email", email, "error", err)
		return c.AbortUnauthorized("saml login failed")
	}
	c.Redirect(http.StatusFound, redirect)
	return nil
}

// identityFromAssertion pulls the email and name out of the assertion using the
// config's attribute mapping, falling back to standard attribute names and the
// NameID for email.
func identityFromAssertion(a *saml.Assertion, cfg *models.SAMLConfig) (email, name string) {
	emailAttr := orDefault(cfg.AttrEmail, "email")
	nameAttr := orDefault(cfg.AttrName, "displayName")
	attrs := map[string]string{}
	for _, stmt := range a.AttributeStatements {
		for _, attr := range stmt.Attributes {
			for _, v := range attr.Values {
				val := strings.TrimSpace(v.Value)
				if val == "" {
					continue
				}
				if attr.Name != "" {
					attrs[attr.Name] = val
				}
				if attr.FriendlyName != "" {
					attrs[attr.FriendlyName] = val
				}
			}
		}
	}
	email = firstNonEmpty(attrs[emailAttr], attrs["email"], attrs["emailAddress"])
	name = firstNonEmpty(attrs[nameAttr], attrs["displayName"], attrs["name"])
	// Fall back to the NameID for email when it looks like an address.
	if email == "" && a.Subject != nil && a.Subject.NameID != nil {
		if id := strings.TrimSpace(a.Subject.NameID.Value); strings.Contains(id, "@") {
			email = id
		}
	}
	return strings.ToLower(email), name
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var errLicense = errors.New("SAML SSO requires an active Enterprise license")
