// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// LDAPAdminHandler manages LDAP / Active Directory connections and their group mappings. Every
// method is gated on the sso_ldap entitlement; Community gets 402. The bind logic lives behind
// the enterprise seam (ee.LDAP()); this handler is DB-only CRUD plus a test proxy.
type LDAPAdminHandler struct {
	ldap  *repositories.LDAPRepository
	ee    enterprise.EE
	audit *audit.Logger
}

func NewLDAPAdminHandler(ldap *repositories.LDAPRepository, ee enterprise.EE, auditLog *audit.Logger) *LDAPAdminHandler {
	return &LDAPAdminHandler{ldap: ldap, ee: ee, audit: auditLog}
}

type LDAPConfigRequest struct {
	Body struct {
		DisplayName     string `json:"display_name" required:"true"`
		Name            string `json:"name"`
		Host            string `json:"host"`
		Port            int    `json:"port"`
		TLSMode         string `json:"tls_mode"`
		CACertPEM       string `json:"ca_cert_pem"`
		InsecureSkipTLS *bool  `json:"insecure_skip_tls"`
		TimeoutSeconds  int    `json:"timeout_seconds"`
		BindDN          string `json:"bind_dn"`
		// BindPassword is the plaintext service-account password. Blank on update
		// preserves the stored secret.
		BindPassword string `json:"bind_password"`
		UserBaseDN   string `json:"user_base_dn"`
		UserFilter   string `json:"user_filter"`
		AttrEmail    string `json:"attr_email"`
		AttrName     string `json:"attr_name"`
		AttrUsername string `json:"attr_username"`
		GroupBaseDN  string `json:"group_base_dn"`
		GroupFilter  string `json:"group_filter"`
		MemberAttr   string `json:"member_attr"`
		NestedGroups *bool  `json:"nested_groups"`
		Enabled      *bool  `json:"enabled"`
	} `json:"body"`
}

func (h *LDAPAdminHandler) List(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagSSOLDAP); err != nil {
		return entitlementAbort(c, err)
	}
	cfgs, err := h.ldap.FindAll()
	if err != nil {
		return c.AbortInternalServerError("failed to list LDAP configs", err)
	}
	return ok(c, cfgs)
}

func (h *LDAPAdminHandler) Create(c *okapi.Context, req *LDAPConfigRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagSSOLDAP); err != nil {
		return entitlementAbort(c, err)
	}
	if strings.TrimSpace(req.Body.Host) == "" {
		return c.AbortBadRequest("host is required")
	}
	if !validTLSMode(req.Body.TLSMode) {
		return c.AbortBadRequest("tls_mode must be none, starttls, or ldaps")
	}
	s := slug.Make(orStr(req.Body.Name, req.Body.DisplayName), "ldap")
	if exists, _ := h.ldap.ExistsByName(s); exists {
		return c.AbortWithError(409, errSlugTaken)
	}
	cfg := &models.LDAPConfig{
		Name:            s,
		DisplayName:     strings.TrimSpace(req.Body.DisplayName),
		Host:            strings.TrimSpace(req.Body.Host),
		Port:            portOrDefault(req.Body.Port, req.Body.TLSMode),
		TLSMode:         tlsModeOrDefault(req.Body.TLSMode),
		CACertPEM:       strings.TrimSpace(req.Body.CACertPEM),
		InsecureSkipTLS: boolOr(req.Body.InsecureSkipTLS, false),
		TimeoutSeconds:  timeoutOrDefault(req.Body.TimeoutSeconds),
		BindDN:          strings.TrimSpace(req.Body.BindDN),
		UserBaseDN:      strings.TrimSpace(req.Body.UserBaseDN),
		UserFilter:      strings.TrimSpace(req.Body.UserFilter),
		AttrEmail:       strings.TrimSpace(req.Body.AttrEmail),
		AttrName:        strings.TrimSpace(req.Body.AttrName),
		AttrUsername:    strings.TrimSpace(req.Body.AttrUsername),
		GroupBaseDN:     strings.TrimSpace(req.Body.GroupBaseDN),
		GroupFilter:     strings.TrimSpace(req.Body.GroupFilter),
		MemberAttr:      strings.TrimSpace(req.Body.MemberAttr),
		NestedGroups:    boolOr(req.Body.NestedGroups, false),
		Enabled:         boolOr(req.Body.Enabled, true),
	}
	if pw := req.Body.BindPassword; pw != "" {
		enc, err := crypto.Encrypt(pw)
		if err != nil {
			return c.AbortInternalServerError("failed to encrypt bind password", err)
		}
		cfg.BindPasswordEnc = enc
	}
	if err := h.ldap.Create(cfg); err != nil {
		return c.AbortInternalServerError("failed to create LDAP config", err)
	}
	cfg.BindPasswordSet = cfg.BindPasswordEnc != ""
	h.record(c, "admin.ldap.create", cfg.Name)
	return created(c, cfg)
}

func (h *LDAPAdminHandler) Update(c *okapi.Context, req *LDAPConfigRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagSSOLDAP); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	cfg, err := h.ldap.FindByID(id)
	if err != nil {
		return c.AbortNotFound("LDAP config not found")
	}
	if req.Body.TLSMode != "" && !validTLSMode(req.Body.TLSMode) {
		return c.AbortBadRequest("tls_mode must be none, starttls, or ldaps")
	}
	if v := strings.TrimSpace(req.Body.DisplayName); v != "" {
		cfg.DisplayName = v
	}
	if v := strings.TrimSpace(req.Body.Name); v != "" {
		cfg.Name = slug.Make(v, cfg.Name)
	}
	if v := strings.TrimSpace(req.Body.Host); v != "" {
		cfg.Host = v
	}
	if req.Body.Port > 0 {
		cfg.Port = req.Body.Port
	}
	if req.Body.TLSMode != "" {
		cfg.TLSMode = req.Body.TLSMode
	}
	if req.Body.TimeoutSeconds > 0 {
		cfg.TimeoutSeconds = req.Body.TimeoutSeconds
	}
	cfg.CACertPEM = strings.TrimSpace(req.Body.CACertPEM)
	cfg.BindDN = strings.TrimSpace(req.Body.BindDN)
	cfg.UserBaseDN = strings.TrimSpace(req.Body.UserBaseDN)
	cfg.UserFilter = strings.TrimSpace(req.Body.UserFilter)
	cfg.AttrEmail = strings.TrimSpace(req.Body.AttrEmail)
	cfg.AttrName = strings.TrimSpace(req.Body.AttrName)
	cfg.AttrUsername = strings.TrimSpace(req.Body.AttrUsername)
	cfg.GroupBaseDN = strings.TrimSpace(req.Body.GroupBaseDN)
	cfg.GroupFilter = strings.TrimSpace(req.Body.GroupFilter)
	cfg.MemberAttr = strings.TrimSpace(req.Body.MemberAttr)
	if req.Body.InsecureSkipTLS != nil {
		cfg.InsecureSkipTLS = *req.Body.InsecureSkipTLS
	}
	if req.Body.NestedGroups != nil {
		cfg.NestedGroups = *req.Body.NestedGroups
	}
	if req.Body.Enabled != nil {
		cfg.Enabled = *req.Body.Enabled
	}
	// A blank bind password preserves the stored secret (mirrors OAuth).
	if pw := req.Body.BindPassword; pw != "" {
		enc, err := crypto.Encrypt(pw)
		if err != nil {
			return c.AbortInternalServerError("failed to encrypt bind password", err)
		}
		cfg.BindPasswordEnc = enc
	}
	if err := h.ldap.Update(cfg); err != nil {
		return c.AbortInternalServerError("failed to update LDAP config", err)
	}
	cfg.BindPasswordSet = cfg.BindPasswordEnc != ""
	h.record(c, "admin.ldap.update", cfg.Name)
	return ok(c, cfg)
}

func (h *LDAPAdminHandler) Delete(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagSSOLDAP); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	if err := h.ldap.Delete(id); err != nil {
		return c.AbortInternalServerError("failed to delete LDAP config", err)
	}
	h.record(c, "admin.ldap.delete", "")
	return message(c, "LDAP config deleted")
}

// TestConnection dials + service-binds the config via the enterprise seam.
func (h *LDAPAdminHandler) TestConnection(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagSSOLDAP); err != nil {
		return entitlementAbort(c, err)
	}
	a := h.ee.LDAP()
	if a == nil {
		return c.AbortWithError(402, enterprise.ErrLicenseRequired)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	res, err := a.TestConnection(c.Request().Context(), id)
	if err != nil {
		return c.AbortNotFound("LDAP config not found")
	}
	return ok(c, res)
}

type LDAPMappingRequest struct {
	Body struct {
		GroupDN       string               `json:"group_dn" required:"true"`
		SystemAdmin   *bool                `json:"system_admin"`
		WorkspaceID   *uint                `json:"workspace_id"`
		WorkspaceRole models.WorkspaceRole `json:"workspace_role"`
	} `json:"body"`
}

func (h *LDAPAdminHandler) CreateMapping(c *okapi.Context, req *LDAPMappingRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagSSOLDAP); err != nil {
		return entitlementAbort(c, err)
	}
	configID, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	if _, err := h.ldap.FindByID(configID); err != nil {
		return c.AbortNotFound("LDAP config not found")
	}
	if strings.TrimSpace(req.Body.GroupDN) == "" {
		return c.AbortBadRequest("group_dn is required")
	}
	if req.Body.WorkspaceRole != "" && !req.Body.WorkspaceRole.Valid() {
		return c.AbortBadRequest("invalid workspace_role")
	}
	m := &models.LDAPGroupMapping{
		LDAPConfigID:  configID,
		GroupDN:       strings.TrimSpace(req.Body.GroupDN),
		SystemAdmin:   boolOr(req.Body.SystemAdmin, false),
		WorkspaceID:   req.Body.WorkspaceID,
		WorkspaceRole: req.Body.WorkspaceRole,
	}
	if err := h.ldap.CreateMapping(m); err != nil {
		return c.AbortInternalServerError("failed to create mapping", err)
	}
	h.record(c, "admin.ldap.mapping.create", m.GroupDN)
	return created(c, m)
}

func (h *LDAPAdminHandler) DeleteMapping(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagSSOLDAP); err != nil {
		return entitlementAbort(c, err)
	}
	configID, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	mappingID, err := uintParam(c, "mappingID")
	if err != nil {
		return c.AbortBadRequest("invalid mapping id")
	}
	if err := h.ldap.DeleteMapping(configID, mappingID); err != nil {
		return c.AbortInternalServerError("failed to delete mapping", err)
	}
	h.record(c, "admin.ldap.mapping.delete", "")
	return message(c, "mapping deleted")
}

func (h *LDAPAdminHandler) record(c *okapi.Context, action, target string) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: action, TargetType: "ldap_config", TargetID: target, IP: c.RealIP()})
}

func validTLSMode(m string) bool {
	switch m {
	case "", models.LDAPTLSNone, models.LDAPTLSStartTLS, models.LDAPTLSLDAPS:
		return true
	default:
		return false
	}
}

func tlsModeOrDefault(m string) string {
	if m == "" {
		return models.LDAPTLSStartTLS
	}
	return m
}

func portOrDefault(port int, tlsMode string) int {
	if port > 0 {
		return port
	}
	if tlsMode == models.LDAPTLSLDAPS {
		return 636
	}
	return 389
}

func timeoutOrDefault(t int) int {
	if t > 0 {
		return t
	}
	return 10
}
