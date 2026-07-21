// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package alerting

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
)

// Signal is a normalized, factual fact from a non-app-event source (backups,
// quotas, nodes, …). Producers emit it via Emitter.Emit and the engine turns it
// into alerts with the same dedup/fan-out machinery as app events. This is the
// generic ingress the plan's §2 describes — adding a source is "emit a Signal",
// never a change to the engine.
//
// A Signal is either a condition (Resolve=false → the fact holds now) or its
// clearance (Resolve=true → the condition ended). Severity/Title/Body are the
// producer's human framing; the engine supplies identity (dedup key) and routing
// (category, min role) from Kind.
type Signal struct {
	WorkspaceID uint
	Kind        string // stable condition id, e.g. "backup_failed", "quota_near"
	Resolve     bool   // true = the condition cleared
	SubjectType string // app | database | node | backup | certificate | quota
	SubjectRef  string // stable subject id, e.g. "database:12"
	SubjectLink string // UI deep-link
	Severity    models.AlertSeverity
	Title       string
	Body        string
	// Platform routes this alert to the super-admins (system workspace) instead of
	// workspace members — set by producers of platform-scoped conditions whose
	// scope is only known at runtime (e.g. a shared vs workspace runner).
	Platform bool
}

// Emitter is the producer-facing handle. The engine implements it; a service that
// detects a condition just calls Emit.
type Emitter interface {
	Emit(sig Signal)
}

// signalRule declares how a Signal Kind maps to an alert: its category, the
// lowest role that receives it, and (for a clearance) the fire Kind(s) it closes.
type signalRule struct {
	category models.AlertCategory
	minRole  models.WorkspaceRole
	// platform forces super-admin fan-out for this Kind regardless of the Signal
	// (e.g. a node is always platform-scoped). A Kind whose scope varies at runtime
	// (shared vs workspace runner) leaves this false and lets Signal.Platform decide.
	platform bool
	// resolves lists the fire Kinds this (clearance) Kind closes. Empty for a
	// fire Kind. A clearance often closes several conditions (a successful backup
	// clears both "backup_failed" and "backup_overdue").
	resolves []string
}

// signalRules is the catalog of Signal-sourced conditions. Fire Kinds carry a
// category + min role; clearance Kinds list what they resolve. Workspace-scoped
// categories reuse the member fan-out directly; platform-scoped ones (node) are
// listed for completeness and wired once a system-admin fan-out exists.
var signalRules = map[string]signalRule{
	// Backups (workspace-scoped: a workspace's database/volume backups).
	"backup_failed":  {category: models.CategoryDatabase, minRole: models.WorkspaceRoleDeveloper},
	"backup_overdue": {category: models.CategoryDatabase, minRole: models.WorkspaceRoleDeveloper},
	"backup_ok":      {resolves: []string{"backup_failed", "backup_overdue"}},

	// TLS / ACME (workspace-scoped: a workspace's certificates).
	"cert_expiring": {category: models.CategoryTLS, minRole: models.WorkspaceRoleDeveloper},
	"cert_failed":   {category: models.CategoryTLS, minRole: models.WorkspaceRoleDeveloper},
	"cert_ok":       {resolves: []string{"cert_expiring", "cert_failed"}},

	// Quotas (workspace-scoped, admin-facing).
	"quota_near": {category: models.CategoryQuota, minRole: models.WorkspaceRoleAdmin},
	"quota_ok":   {resolves: []string{"quota_near"}},

	// Storage / disk (workspace-scoped: a workspace's volumes near capacity).
	"disk_near": {category: models.CategoryStorage, minRole: models.WorkspaceRoleDeveloper},
	"disk_ok":   {resolves: []string{"disk_near"}},

	// Nodes (always platform-scoped → super-admins).
	"node_offline": {category: models.CategoryNode, minRole: models.WorkspaceRoleViewer, platform: true},
	"node_online":  {resolves: []string{"node_offline"}},

	// Runners (CI). Scope varies at runtime: a workspace runner notifies its
	// members; a shared runner is platform-scoped (Signal.Platform=true → admins).
	"runner_offline": {category: models.CategoryCI, minRole: models.WorkspaceRoleDeveloper},
	"runner_online":  {resolves: []string{"runner_offline"}},
}

// dedupKey is the alert identity for a signal: "<kind>:<subjectRef>".
func signalDedup(kind, subjectRef string) string {
	return fmt.Sprintf("%s:%s", kind, subjectRef)
}

// evaluateSignal maps a Signal to alert intents (pure). A fire Signal yields one
// fire intent; a clearance yields a resolve intent per Kind it closes.
func evaluateSignal(sig Signal) []intent {
	rule, ok := signalRules[sig.Kind]
	if !ok || sig.SubjectRef == "" {
		return nil
	}
	if sig.Resolve || len(rule.resolves) > 0 {
		out := make([]intent, 0, len(rule.resolves))
		for _, fireKind := range rule.resolves {
			out = append(out, intent{kind: resolve, dedupKey: signalDedup(fireKind, sig.SubjectRef)})
		}
		return out
	}
	sev := sig.Severity
	if sev == "" {
		sev = models.AlertWarning
	}
	return []intent{{
		kind:        fire,
		ruleKey:     sig.Kind,
		dedupKey:    signalDedup(sig.Kind, sig.SubjectRef),
		category:    rule.category,
		severity:    sev,
		subjectType: sig.SubjectType,
		subjectRef:  sig.SubjectRef,
		subjectLink: sig.SubjectLink,
		minRole:     rule.minRole,
		title:       sig.Title,
		body:        sig.Body,
		platform:    rule.platform || sig.Platform,
	}}
}

// Emit routes a producer's Signal through the engine (implements Emitter).
func (e *Engine) Emit(sig Signal) {
	if sig.WorkspaceID == 0 {
		return
	}
	for _, in := range evaluateSignal(sig) {
		switch in.kind {
		case fire:
			e.doFire(context.Background(), sig.WorkspaceID, in)
		case resolve:
			e.doResolve(context.Background(), sig.WorkspaceID, in.dedupKey)
		}
	}
}
