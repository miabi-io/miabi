// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// SSOAdminHandler manages the identity realm: the organization's enforced-SSO
// toggle and SAML 2.0 connections. SAML config and enforcing SSO are gated on
// the sso_saml entitlement (Enterprise); a Community/unlicensed install gets 402.
type SSOAdminHandler struct {
	orgs       *repositories.OrganizationRepository
	saml       *repositories.SAMLConfigRepository
	scimTokens *repositories.SCIMTokenRepository
	ee         enterprise.EE
	audit      *audit.Logger
}

func NewSSOAdminHandler(orgs *repositories.OrganizationRepository, samlCfg *repositories.SAMLConfigRepository, scimTokens *repositories.SCIMTokenRepository, ee enterprise.EE, auditLog *audit.Logger) *SSOAdminHandler {
	return &SSOAdminHandler{orgs: orgs, saml: samlCfg, scimTokens: scimTokens, ee: ee, audit: auditLog}
}

// GetOrganization returns the default organization.
func (h *SSOAdminHandler) GetOrganization(c *okapi.Context) error {
	org, err := h.orgs.FindDefault()
	if err != nil {
		return c.AbortNotFound("organization not found")
	}
	return ok(c, org)
}

type UpdateOrganizationRequest struct {
	Body struct {
		DisplayName *string `json:"display_name"`
		EnforceSSO  *bool   `json:"enforce_sso"`
	} `json:"body"`
}

// UpdateOrganization edits the default org. Turning on enforced SSO requires the
// sso_saml entitlement (you cannot force SSO without a SAML connection); turning
// it off is always allowed (lock-out safety valve).
func (h *SSOAdminHandler) UpdateOrganization(c *okapi.Context, req *UpdateOrganizationRequest) error {
	org, err := h.orgs.FindDefault()
	if err != nil {
		return c.AbortNotFound("organization not found")
	}
	if req.Body.DisplayName != nil {
		org.DisplayName = strings.TrimSpace(*req.Body.DisplayName)
	}
	if req.Body.EnforceSSO != nil {
		if *req.Body.EnforceSSO {
			if err := h.ee.RequireMutable(enterprise.FlagSSOSAML); err != nil {
				return entitlementAbort(c, err)
			}
		}
		org.EnforceSSO = *req.Body.EnforceSSO
	}
	if err := h.orgs.Update(org); err != nil {
		return c.AbortInternalServerError("failed to update organization", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: "admin.organization.update", TargetType: "organization", IP: c.RealIP(), Metadata: map[string]any{"enforce_sso": org.EnforceSSO}})
	return ok(c, org)
}

type SAMLConfigRequest struct {
	Body struct {
		DisplayName    string `json:"display_name" required:"true"`
		Name           string `json:"name"`
		IDPMetadataURL string `json:"idp_metadata_url"`
		IDPMetadataXML string `json:"idp_metadata_xml"`
		SPEntityID     string `json:"sp_entity_id"`
		AttrEmail      string `json:"attr_email"`
		AttrName       string `json:"attr_name"`
		Enabled        *bool  `json:"enabled"`
	} `json:"body"`
}

func (h *SSOAdminHandler) ListSAML(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagSSOSAML); err != nil {
		return entitlementAbort(c, err)
	}
	cfgs, err := h.saml.FindAll()
	if err != nil {
		return c.AbortInternalServerError("failed to list SAML configs", err)
	}
	return ok(c, cfgs)
}

func (h *SSOAdminHandler) CreateSAML(c *okapi.Context, req *SAMLConfigRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagSSOSAML); err != nil {
		return entitlementAbort(c, err)
	}
	if strings.TrimSpace(req.Body.IDPMetadataURL) == "" && strings.TrimSpace(req.Body.IDPMetadataXML) == "" {
		return c.AbortBadRequest("provide idp_metadata_url or idp_metadata_xml")
	}
	s := slug.Make(orStr(req.Body.Name, req.Body.DisplayName), "saml")
	if exists, _ := h.saml.ExistsByName(s); exists {
		return c.AbortWithError(409, errSlugTaken)
	}
	cfg := &models.SAMLConfig{
		Name: s, DisplayName: strings.TrimSpace(req.Body.DisplayName),
		IDPMetadataURL: strings.TrimSpace(req.Body.IDPMetadataURL),
		IDPMetadataXML: strings.TrimSpace(req.Body.IDPMetadataXML),
		SPEntityID:     strings.TrimSpace(req.Body.SPEntityID),
		AttrEmail:      strings.TrimSpace(req.Body.AttrEmail),
		AttrName:       strings.TrimSpace(req.Body.AttrName),
		Enabled:        boolOr(req.Body.Enabled, true),
	}
	if err := h.saml.Create(cfg); err != nil {
		return c.AbortInternalServerError("failed to create SAML config", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: "admin.saml.create", TargetType: "saml_config", TargetID: cfg.Name, IP: c.RealIP()})
	return created(c, cfg)
}

func (h *SSOAdminHandler) UpdateSAML(c *okapi.Context, req *SAMLConfigRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagSSOSAML); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	cfg, err := h.saml.FindByID(id)
	if err != nil {
		return c.AbortNotFound("SAML config not found")
	}
	if v := strings.TrimSpace(req.Body.DisplayName); v != "" {
		cfg.DisplayName = v
	}
	if v := strings.TrimSpace(req.Body.Name); v != "" {
		cfg.Name = slug.Make(v, cfg.Name)
	}
	cfg.IDPMetadataURL = strings.TrimSpace(req.Body.IDPMetadataURL)
	cfg.IDPMetadataXML = strings.TrimSpace(req.Body.IDPMetadataXML)
	cfg.SPEntityID = strings.TrimSpace(req.Body.SPEntityID)
	cfg.AttrEmail = strings.TrimSpace(req.Body.AttrEmail)
	cfg.AttrName = strings.TrimSpace(req.Body.AttrName)
	if req.Body.Enabled != nil {
		cfg.Enabled = *req.Body.Enabled
	}
	if err := h.saml.Update(cfg); err != nil {
		return c.AbortInternalServerError("failed to update SAML config", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: "admin.saml.update", TargetType: "saml_config", TargetID: cfg.Name, IP: c.RealIP()})
	return ok(c, cfg)
}

func (h *SSOAdminHandler) DeleteSAML(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagSSOSAML); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	if err := h.saml.Delete(id); err != nil {
		return c.AbortInternalServerError("failed to delete SAML config", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: "admin.saml.delete", TargetType: "saml_config", IP: c.RealIP()})
	return message(c, "SAML config deleted")
}

func (h *SSOAdminHandler) ListSCIMTokens(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagSCIM); err != nil {
		return entitlementAbort(c, err)
	}
	tokens, err := h.scimTokens.FindAll()
	if err != nil {
		return c.AbortInternalServerError("failed to list SCIM tokens", err)
	}
	return ok(c, tokens)
}

type CreateSCIMTokenRequest struct {
	Body struct {
		Name string `json:"name" required:"true"`
	} `json:"body"`
}

// CreateSCIMToken mints a bearer token, storing only its hash and returning the
// plaintext once (the IdP configures it as the SCIM credential).
func (h *SSOAdminHandler) CreateSCIMToken(c *okapi.Context, req *CreateSCIMTokenRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagSCIM); err != nil {
		return entitlementAbort(c, err)
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return c.AbortBadRequest("name is required")
	}
	raw := "scim_" + randomHex(24)
	tok := &models.SCIMToken{Name: name, TokenHash: repositories.HashSCIMToken(raw)}
	if err := h.scimTokens.Create(tok); err != nil {
		return c.AbortInternalServerError("failed to create SCIM token", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: "admin.scim.token.create", TargetType: "scim_token", IP: c.RealIP(), Metadata: map[string]any{"name": name}})
	return created(c, map[string]any{"id": tok.ID, "name": tok.Name, "token": raw})
}

func (h *SSOAdminHandler) DeleteSCIMToken(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagSCIM); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	if err := h.scimTokens.Delete(id); err != nil {
		return c.AbortInternalServerError("failed to delete SCIM token", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: "admin.scim.token.delete", TargetType: "scim_token", IP: c.RealIP()})
	return message(c, "SCIM token revoked")
}

func orStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
