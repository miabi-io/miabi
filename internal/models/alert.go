// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// AlertSeverity ranks an alert's urgency. Only warning/critical notify by
// default; info lives in the inbox.
type AlertSeverity string

const (
	AlertInfo     AlertSeverity = "info"
	AlertWarning  AlertSeverity = "warning"
	AlertCritical AlertSeverity = "critical"
)

// AlertState is the alert's lifecycle position. An alert is a small FSM, not a
// log row: it fires, may be acknowledged, auto-resolves when the condition
// clears, and is archived after a retention window.
type AlertState string

const (
	AlertFiring       AlertState = "firing"
	AlertAcknowledged AlertState = "acknowledged"
	AlertResolved     AlertState = "resolved"
	AlertArchived     AlertState = "archived"
)

// Active reports whether the alert is still an open condition (firing or
// acknowledged) — the states the dedup key is unique across.
func (s AlertState) Active() bool { return s == AlertFiring || s == AlertAcknowledged }

// AlertCategory groups alerts for role-based fan-out and UI filtering.
type AlertCategory string

const (
	CategoryDeploy   AlertCategory = "deploy"
	CategoryRuntime  AlertCategory = "runtime"
	CategoryDatabase AlertCategory = "database"
	CategoryStorage  AlertCategory = "storage"
	CategoryTLS      AlertCategory = "tls"
	CategoryNode     AlertCategory = "node"
	CategoryCI       AlertCategory = "ci" // runners / pipelines
	CategoryQuota    AlertCategory = "quota"
	CategorySecurity AlertCategory = "security"
)

// Alert is a workspace-level, stateful, deduplicated condition derived from
// signals (app events, metrics, jobs). It is shared across the workspace's
// members; per-user delivery is a Notification. The dedup key is the identity of
// the condition (e.g. "crashloop:app:42"): a repeat signal updates the existing
// active alert (bumping Count/LastSeen) instead of creating a new row, and a
// recovery signal resolves it — this is what turns 40 crash events into one line.
type Alert struct {
	ID          uint          `json:"id" gorm:"primaryKey"`
	WorkspaceID uint          `json:"workspace_id" gorm:"index;not null"`
	RuleKey     string        `json:"rule_key" gorm:"not null"` // which built-in rule fired
	Category    AlertCategory `json:"category" gorm:"not null"`
	Severity    AlertSeverity `json:"severity" gorm:"not null"`
	State       AlertState    `json:"state" gorm:"not null;default:firing"`

	// DedupKey identifies the condition+subject. The partial unique index in the
	// migration enforces at most one ACTIVE alert per (workspace, dedup_key).
	DedupKey    string `json:"dedup_key" gorm:"not null"`
	SubjectType string `json:"subject_type"`           // app | database | node | route | backup
	SubjectRef  string `json:"subject_ref"`            // stable id/slug of the subject
	SubjectLink string `json:"subject_link,omitempty"` // UI deep-link
	// MinRole is the lowest workspace role that receives this alert (fan-out gate).
	MinRole WorkspaceRole `json:"min_role" gorm:"not null;default:developer"`

	Title  string            `json:"title"`
	Body   string            `json:"body"`
	Count  int64             `json:"count" gorm:"not null;default:1"` // signals folded into this alert
	Labels map[string]string `json:"labels,omitempty" gorm:"serializer:json"`

	FirstSeen  time.Time  `json:"first_seen"`
	LastSeen   time.Time  `json:"last_seen"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
