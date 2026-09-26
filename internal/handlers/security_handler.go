// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/portbinding"
	"github.com/miabi-io/miabi/internal/services/secpolicy"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// SecurityHandler serves the Security Center (Admin → Security) and the workspace-facing view of
// the policies a tenant is subject to.
type SecurityHandler struct {
	svc        *secpolicy.Service
	ports      *portbinding.Service
	workspaces *repositories.WorkspaceRepository
	users      *repositories.UserRepository
	ee         enterprise.EE
}

func NewSecurityHandler(svc *secpolicy.Service, ports *portbinding.Service, workspaces *repositories.WorkspaceRepository, users *repositories.UserRepository, ee enterprise.EE) *SecurityHandler {
	return &SecurityHandler{svc: svc, ports: ports, workspaces: workspaces, users: users, ee: ee}
}

// SecurityStatus tells the console what it can offer. Reads are ungated so Community shows the
// page with an upgrade prompt rather than an error.
type SecurityStatus struct {
	Entitled bool `json:"entitled"`
	// Mutable is false once a licence is past grace: policies keep applying, edits are frozen.
	Mutable bool `json:"mutable"`
	// Enabled is false when MIABI_SECURITY_POLICIES=off ignores stored policies.
	Enabled bool `json:"enabled"`
}

func (h *SecurityHandler) status() SecurityStatus {
	return SecurityStatus{
		Entitled: h.ee.Require(enterprise.FlagSecurityPolicies) == nil,
		Mutable:  h.ee.RequireMutable(enterprise.FlagSecurityPolicies) == nil,
		Enabled:  h.svc.Enabled(),
	}
}

// Status reports entitlement and the kill switch.
func (h *SecurityHandler) Status(c *okapi.Context) error { return ok(c, h.status()) }

// ListPolicies returns every stored rule.
func (h *SecurityHandler) ListPolicies(c *okapi.Context) error {
	list, err := h.svc.List()
	if err != nil {
		return c.AbortInternalServerError("failed to list security policies", err)
	}
	return ok(c, list)
}

// SavePolicyRequest upserts the rule for one kind at one scope.
type SavePolicyRequest struct {
	Body struct {
		Kind            string `json:"kind" required:"true" enum:"ports,admin_access"`
		ScopeType       string `json:"scope_type" required:"true" enum:"platform,plan,organization,workspace"`
		ScopeID         uint   `json:"scope_id"`
		Mode            string `json:"mode" required:"true" enum:"off,audit,enforce"`
		AllowExceptions bool   `json:"allow_exceptions"`
		// Spec is the kind's JSON spec, e.g. {"v":1,"mode":"reject_all"} for ports.
		Spec string `json:"spec"`
	} `json:"body"`
}

// SavePolicy creates or replaces a rule (Enterprise; security_policies).
func (h *SecurityHandler) SavePolicy(c *okapi.Context, req *SavePolicyRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagSecurityPolicies); err != nil {
		return entitlementAbort(c, err)
	}
	if msg := h.lockoutGuard(c, req); msg != "" {
		return c.AbortBadRequest(msg)
	}
	p, err := h.svc.Save(middlewares.UserID(c), c.RealIP(), secpolicy.SaveInput{
		Kind: req.Body.Kind, ScopeType: req.Body.ScopeType, ScopeID: req.Body.ScopeID,
		Mode: req.Body.Mode, AllowExceptions: req.Body.AllowExceptions, Spec: req.Body.Spec,
	})
	switch {
	case errors.Is(err, secpolicy.ErrInvalidSpec), errors.Is(err, secpolicy.ErrLoosens):
		return c.AbortBadRequest(err.Error())
	case err != nil:
		return c.AbortInternalServerError("failed to save the security policy", err)
	}
	return ok(c, p)
}

// lockoutGuard refuses an enforced, required console unlock from an admin who could not satisfy it
// themselves, which would leave the console behind a factor nobody saving it has.
func (h *SecurityHandler) lockoutGuard(c *okapi.Context, req *SavePolicyRequest) string {
	if req.Body.Kind != secpolicy.KindAdminAccess || req.Body.Mode != secpolicy.ModeEnforce {
		return ""
	}
	_, a, err := secpolicy.ParseAdminAccessSpec(req.Body.Spec)
	if err != nil || !a.Required {
		return ""
	}
	u, uerr := h.users.FindByID(middlewares.UserID(c))
	if uerr != nil || !u.TwoFactorEnabled {
		return "set up two-factor authentication on your own account before requiring it for the admin console"
	}
	return ""
}

// DeletePolicy removes a rule. Ungated: an expired licence must still be able to relax a rule
// that is in the way.
func (h *SecurityHandler) DeletePolicy(c *okapi.Context) error {
	id, err := strconv.Atoi(c.Param("policyID"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid policy id")
	}
	if err := h.svc.Delete(middlewares.UserID(c), c.RealIP(), uint(id)); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.AbortNotFound("security policy not found")
		}
		return c.AbortInternalServerError("failed to delete the security policy", err)
	}
	return ok(c, okapi.M{"deleted": true})
}

// SecurityOverview is the posture summary on the Overview tab.
type SecurityOverview struct {
	SecurityStatus
	Events24h            []repositories.EventCount `json:"events_24h"`
	PrivilegedWorkspaces int                       `json:"privileged_workspaces"`
	Ports                PortsPosture              `json:"ports"`
}

// PortsPosture summarises host ports against the platform rule.
type PortsPosture struct {
	Policy      *models.SecurityPolicy `json:"policy,omitempty"`
	Spec        *secpolicy.PortsSpec   `json:"spec,omitempty"`
	Approved    int                    `json:"approved"`
	AllIfaces   int                    `json:"all_interfaces"`
	Pending     int                    `json:"pending"`
	Grandfather int                    `json:"grandfathered"`
	// Bindings lists approved bindings when the rule reports or revokes existing ones.
	Bindings []portbinding.ApprovedBinding `json:"bindings,omitempty"`
}

// Overview reports posture and the last 24 hours of decisions.
func (h *SecurityHandler) Overview(c *okapi.Context) error {
	out := SecurityOverview{SecurityStatus: h.status()}
	counts, err := h.svc.CountSince(time.Now().Add(-24 * time.Hour))
	if err != nil {
		return c.AbortInternalServerError("failed to count security events", err)
	}
	out.Events24h = counts
	if all, werr := h.workspaces.ListAll(); werr == nil {
		for i := range all {
			if all[i].Privileged {
				out.PrivilegedWorkspaces++
			}
		}
	}
	approved, err := h.ports.ListApproved()
	if err != nil {
		return c.AbortInternalServerError("failed to list host ports", err)
	}
	pending, _ := h.ports.ListByStatus(models.PortBindingPending)
	pol, spec := h.svc.PlatformPorts()
	out.Ports = PortsPosture{Policy: pol, Spec: spec, Approved: len(approved), Pending: len(pending)}
	bound := spec != nil && pol.Mode == secpolicy.ModeEnforce && spec.BindAddress != ""
	if !bound {
		out.Ports.AllIfaces = len(approved)
	}
	if spec != nil && spec.Mode == secpolicy.PortsRejectAll {
		for _, b := range approved {
			if !b.UpdatedAt.After(pol.UpdatedAt) {
				out.Ports.Grandfather++
			}
		}
	}
	if spec != nil && spec.Existing != secpolicy.PortsExistingKeep {
		out.Ports.Bindings = approved
	}
	return ok(c, out)
}

// RevokePortsRequest confirms a revoke. DryRun lists the affected bindings and changes nothing,
// so the console can show every affected app before the admin confirms.
type RevokePortsRequest struct {
	Body struct {
		DryRun bool `json:"dry_run"`
	} `json:"body"`
}

// RevokePorts rejects every approved host-port binding (Enterprise). Only offered while the
// platform rule rejects host ports and says existing bindings are revoked.
func (h *SecurityHandler) RevokePorts(c *okapi.Context, req *RevokePortsRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagSecurityPolicies); err != nil {
		return entitlementAbort(c, err)
	}
	pol, spec := h.svc.PlatformPorts()
	if spec == nil || pol.Mode != secpolicy.ModeEnforce || spec.Mode != secpolicy.PortsRejectAll || spec.Existing != secpolicy.PortsExistingRevoke {
		return c.AbortBadRequest("revoking applies only while the platform host-port rule is enforced as reject_all with existing: revoke")
	}
	list, err := h.ports.RevokeApproved(middlewares.UserID(c), req.Body.DryRun)
	if err != nil {
		return c.AbortInternalServerError("failed to revoke host ports", err)
	}
	return ok(c, okapi.M{"dry_run": req.Body.DryRun, "bindings": list})
}

func (h *SecurityHandler) eventFilter(c *okapi.Context) repositories.EventFilter {
	f := repositories.EventFilter{Kind: c.Query("kind"), Decision: c.Query("decision"), Limit: 100}
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 && n <= 1000 {
		f.Limit = n
	}
	if n, err := strconv.Atoi(c.Query("before")); err == nil && n > 0 {
		f.BeforeID = uint(n)
	}
	if n, err := strconv.Atoi(c.Query("workspace_id")); err == nil && n > 0 {
		f.WorkspaceID = uint(n)
	}
	return f
}

// Events lists recorded decisions, newest first. Query: kind, decision, workspace_id, before, limit.
func (h *SecurityHandler) Events(c *okapi.Context) error {
	list, err := h.svc.Events(h.eventFilter(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list security events", err)
	}
	return ok(c, list)
}

// ExportEvents downloads the filtered decisions as CSV.
func (h *SecurityHandler) ExportEvents(c *okapi.Context) error {
	f := h.eventFilter(c)
	if n, err := strconv.Atoi(c.Query("limit")); err != nil || n <= 0 {
		f.Limit = 10000
	}
	list, err := h.svc.Events(f)
	if err != nil {
		return c.AbortInternalServerError("failed to export security events", err)
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"id", "time", "kind", "action", "decision", "user_id", "workspace_id", "resource", "policy_id", "scope", "reason"})
	opt := func(v *uint) string {
		if v == nil {
			return ""
		}
		return strconv.FormatUint(uint64(*v), 10)
	}
	for _, e := range list {
		_ = w.Write([]string{strconv.FormatUint(uint64(e.ID), 10), e.CreatedAt.UTC().Format(time.RFC3339), e.Kind, e.Action,
			e.Decision, opt(e.UserID), opt(e.WorkspaceID), e.Resource, opt(e.PolicyID), e.Scope, e.Reason})
	}
	w.Flush()
	c.SetHeader("Content-Type", "text/csv; charset=utf-8")
	c.SetHeader("Content-Disposition", `attachment; filename="security-events.csv"`)
	c.Response().WriteHeader(http.StatusOK)
	_, _ = c.Response().Write([]byte(b.String()))
	return nil
}

// WorkspacePortsPolicy tells a workspace member whether host-port requests are open, so the app
// page can hide "Request host port" and say why.
func (h *SecurityHandler) WorkspacePortsPolicy(c *okapi.Context) error {
	return ok(c, h.svc.PortsView(middlewares.WorkspaceID(c)))
}
