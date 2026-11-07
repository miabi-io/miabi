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

package alerting

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
)

// Signal carries non-app-event facts through the same dedup and fan-out as app events.
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
	// For producers whose scope is only known at runtime (shared vs workspace runner).
	Platform bool
}

type Emitter interface {
	Emit(sig Signal)
}

type signalRule struct {
	category models.AlertCategory
	minRole  models.WorkspaceRole
	// Kinds whose scope varies at runtime leave this false and let Signal.Platform decide.
	platform bool
	// A clearance often closes several conditions (backup_ok clears backup_failed and backup_overdue).
	resolves []string
}

var signalRules = map[string]signalRule{
	// Backups (workspace-scoped: a workspace's database/volume backups).
	"backup_failed":  {category: models.CategoryDatabase, minRole: models.WorkspaceRoleDeveloper},
	"backup_overdue": {category: models.CategoryDatabase, minRole: models.WorkspaceRoleDeveloper},
	"backup_ok":      {resolves: []string{"backup_failed", "backup_overdue"}},

	"backup_set_failed": {category: models.CategoryDatabase, minRole: models.WorkspaceRoleDeveloper},
	"backup_set_ok":     {resolves: []string{"backup_set_failed"}},

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

	// Scope varies at runtime: shared runners set Signal.Platform to notify admins.
	"runner_offline": {category: models.CategoryCI, minRole: models.WorkspaceRoleDeveloper},
	"runner_online":  {resolves: []string{"runner_offline"}},
}

// dedupKey is the alert identity for a signal: "<kind>:<subjectRef>".
func signalDedup(kind, subjectRef string) string {
	return fmt.Sprintf("%s:%s", kind, subjectRef)
}

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
