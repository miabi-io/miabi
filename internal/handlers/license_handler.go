// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
)

// nearingExpiryWindow is how far before NotAfter a "nearing_expiry" warning is
// raised so admins renew before the grace/degrade transition.
const nearingExpiryWindow = 30 * 24 * time.Hour

// LicenseHandler exposes the commercial license: install, view, remove, and the health summary
// driving the web banner. It works in both editions — in Community the injected enterprise.EE
// is the deny-all stub, so install returns 402 and the view reports edition "community".
type LicenseHandler struct {
	ee        enterprise.EE
	nodeCount func() int64
	planCount func() int64
	installID string // this deployment's stable Install ID ("Your Install ID")
	audit     *audit.Logger
}

func NewLicenseHandler(ee enterprise.EE, nodeCount, planCount func() int64, installID string, auditLog *audit.Logger) *LicenseHandler {
	return &LicenseHandler{ee: ee, nodeCount: nodeCount, planCount: planCount, installID: installID, audit: auditLog}
}

// NodeUsage reports active nodes against the licensed cap (-1 = unlimited).
type NodeUsage struct {
	Used  int64 `json:"used"`
	Limit int   `json:"limit"`
}

// PlanUsage reports the plan-catalog size against the edition cap (-1 = unlimited).
type PlanUsage struct {
	Used  int64 `json:"used"`
	Limit int   `json:"limit"`
}

// LicenseView is the API representation of the current license: the resolved
// entitlements plus node/plan usage, this instance's Install ID, and any operator
// warnings.
type LicenseView struct {
	enterprise.Entitlements
	// InstanceInstallID is THIS deployment's stable Install ID ("Your Install ID"),
	// shown so a customer can copy it when purchasing a license. Distinct from the
	// embedded Entitlements.InstallID (the id a license is bound to).
	InstanceInstallID string    `json:"instance_install_id"`
	NodeUsage         NodeUsage `json:"node_usage"`
	PlanUsage         PlanUsage `json:"plan_usage"`
	Warnings          []string  `json:"warnings"`
}

type InstallLicenseRequest struct {
	Body struct {
		Token string `json:"token" required:"true"`
	} `json:"body"`
}

func (h *LicenseHandler) view() LicenseView {
	ent := h.ee.Entitlements()
	used := int64(0)
	if h.nodeCount != nil {
		used = h.nodeCount()
	}
	plans := int64(0)
	if h.planCount != nil {
		plans = h.planCount()
	}
	return LicenseView{
		Entitlements:      ent,
		InstanceInstallID: h.installID,
		NodeUsage:         NodeUsage{Used: used, Limit: ent.NodeLimit()},
		PlanUsage:         PlanUsage{Used: plans, Limit: ent.PlanLimit()},
		Warnings:          warnings(ent, used),
	}
}

func warnings(ent enterprise.Entitlements, nodesUsed int64) []string {
	out := []string{}
	switch ent.State {
	case "grace":
		out = append(out, "license_grace")
	case "degraded":
		out = append(out, "license_expired")
	case "valid":
		if ent.NotAfter != nil && time.Until(*ent.NotAfter) < nearingExpiryWindow {
			out = append(out, "nearing_expiry")
		}
	}
	if limit := ent.NodeLimit(); limit >= 0 && nodesUsed > int64(limit) {
		out = append(out, "over_node_limit")
	}
	return out
}

// Get returns the current license, entitlements, node usage, and warnings.
func (h *LicenseHandler) Get(c *okapi.Context) error {
	return ok(c, h.view())
}

// Health returns just the warnings list (drives the global UI banner).
func (h *LicenseHandler) Health(c *okapi.Context) error {
	ent := h.ee.Entitlements()
	used := int64(0)
	if h.nodeCount != nil {
		used = h.nodeCount()
	}
	return ok(c, map[string]any{"state": ent.State, "edition": ent.Edition, "warnings": warnings(ent, used)})
}

// Install verifies and persists a signed license token. In Community the stub
// returns ErrCommunityEdition → 402; a bad/forged token → 400.
func (h *LicenseHandler) Install(c *okapi.Context, req *InstallLicenseRequest) error {
	token := strings.TrimSpace(req.Body.Token)
	if token == "" {
		return c.AbortBadRequest("license token is required")
	}
	ent, err := h.ee.Install(c.Request().Context(), token, requestHost(c))
	if err != nil {
		if resp := entitlementAbort(c, err); resp != nil {
			return resp
		}
		return c.AbortBadRequest("invalid license token", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: "admin.license.install", TargetType: "license", TargetID: ent.LicenseID,
		IP: c.RealIP(), Metadata: map[string]any{"edition": ent.Edition, "customer": ent.Customer},
	})
	return h.Get(c)
}

// requestHost returns the host the request was made to — the live deployment identity used to
// validate a URL-bound license at install. It honors X-Forwarded-Host and takes the first value
// of a comma-separated list; normalization to a bare hostname happens downstream.
func requestHost(c *okapi.Context) string {
	r := c.Request()
	host := r.Host
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); fwd != "" {
		host = fwd
	}
	if i := strings.IndexByte(host, ','); i >= 0 {
		host = strings.TrimSpace(host[:i])
	}
	return host
}

// Delete removes the installed license, reverting to Community.
func (h *LicenseHandler) Delete(c *okapi.Context) error {
	prev := h.ee.Entitlements()
	if err := h.ee.Remove(c.Request().Context()); err != nil {
		if resp := entitlementAbort(c, err); resp != nil {
			return resp
		}
		return c.AbortInternalServerError("failed to remove license", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: "admin.license.remove", TargetType: "license", TargetID: prev.LicenseID,
		IP: c.RealIP(), Metadata: map[string]any{"edition": prev.Edition},
	})
	return message(c, "license removed")
}
