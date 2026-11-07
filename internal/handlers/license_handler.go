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

package handlers

import (
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
)

const nearingExpiryWindow = 30 * 24 * time.Hour

// In Community the injected EE is the deny-all stub, so install returns 402.
type LicenseHandler struct {
	ee                enterprise.EE
	nodeCount         func() int64
	planCount         func() int64
	storageClassCount func() int64
	sharedRunnerCount func() int64
	installID         string // this deployment's stable Install ID ("Your Install ID")
	audit             *audit.Logger
}

func NewLicenseHandler(ee enterprise.EE, nodeCount, planCount, storageClassCount, sharedRunnerCount func() int64, installID string, auditLog *audit.Logger) *LicenseHandler {
	return &LicenseHandler{
		ee: ee, nodeCount: nodeCount, planCount: planCount, storageClassCount: storageClassCount,
		sharedRunnerCount: sharedRunnerCount, installID: installID, audit: auditLog,
	}
}

// NodeUsage reports registered nodes, excluding the control plane, against the licensed cap (-1 = unlimited).
type NodeUsage struct {
	Used  int64 `json:"used"`
	Limit int   `json:"limit"`
}

// PlanUsage reports the plan-catalog size against the edition cap (-1 = unlimited).
type PlanUsage struct {
	Used  int64 `json:"used"`
	Limit int   `json:"limit"`
}

type StorageClassUsage struct {
	Used  int64 `json:"used"`
	Limit int   `json:"limit"`
}

// Workspace-owned runners are bounded by the workspace plan, not here.
type SharedRunnerUsage struct {
	Used  int64 `json:"used"`
	Limit int   `json:"limit"`
}

type LicenseView struct {
	enterprise.Entitlements
	// Distinct from Entitlements.InstallID, which is the id a license is bound to.
	InstanceInstallID string            `json:"instance_install_id"`
	NodeUsage         NodeUsage         `json:"node_usage"`
	PlanUsage         PlanUsage         `json:"plan_usage"`
	StorageClassUsage StorageClassUsage `json:"storage_class_usage"`
	SharedRunnerUsage SharedRunnerUsage `json:"shared_runner_usage"`
	Warnings          []string          `json:"warnings"`
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
	classes := int64(0)
	if h.storageClassCount != nil {
		classes = h.storageClassCount()
	}
	runners := int64(0)
	if h.sharedRunnerCount != nil {
		runners = h.sharedRunnerCount()
	}
	return LicenseView{
		Entitlements:      ent,
		InstanceInstallID: h.installID,
		NodeUsage:         NodeUsage{Used: used, Limit: ent.NodeLimit()},
		PlanUsage:         PlanUsage{Used: plans, Limit: ent.PlanLimit()},
		StorageClassUsage: StorageClassUsage{Used: classes, Limit: ent.StorageClassLimit()},
		SharedRunnerUsage: SharedRunnerUsage{Used: runners, Limit: ent.SharedRunnerLimit()},
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

type LicenseEntitlements struct {
	Edition string          `json:"edition"`
	Tier    string          `json:"tier,omitempty"`
	State   string          `json:"state"`
	Flags   map[string]bool `json:"flags"`
}

func (h *LicenseHandler) Entitlements(c *okapi.Context) error {
	ent := h.ee.Entitlements()
	return ok(c, LicenseEntitlements{Edition: ent.Edition, Tier: ent.Tier, State: ent.State, Flags: ent.Flags})
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

// Honors X-Forwarded-Host (first value); normalization to a bare hostname happens downstream.
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
