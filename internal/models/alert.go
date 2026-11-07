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

package models

import "time"

// Only warning and critical notify by default; info lives in the inbox.
type AlertSeverity string

const (
	AlertInfo     AlertSeverity = "info"
	AlertWarning  AlertSeverity = "warning"
	AlertCritical AlertSeverity = "critical"
)

// A small FSM: fires, may be acknowledged, auto-resolves when cleared, archived after retention.
type AlertState string

const (
	AlertFiring       AlertState = "firing"
	AlertAcknowledged AlertState = "acknowledged"
	AlertResolved     AlertState = "resolved"
	AlertArchived     AlertState = "archived"
)

// The dedup key is unique across these states.
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

// A repeat signal bumps Count/LastSeen instead of adding a row.
type Alert struct {
	ID          uint          `json:"id" gorm:"primaryKey"`
	WorkspaceID uint          `json:"workspace_id" gorm:"index;not null"`
	RuleKey     string        `json:"rule_key" gorm:"not null"` // which built-in rule fired
	Category    AlertCategory `json:"category" gorm:"not null"`
	Severity    AlertSeverity `json:"severity" gorm:"not null"`
	State       AlertState    `json:"state" gorm:"not null;default:firing"`

	// A partial unique index enforces at most one active alert per (workspace, dedup_key).
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
