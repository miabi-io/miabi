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

// Columns default to 0 because GORM drops a zero on insert when its column has another default; see DefaultRegistryRetention.
type RegistryRetention struct {
	ID      uint `json:"-" gorm:"primaryKey"`
	Enabled bool `json:"enabled" gorm:"not null;default:false"`
	// KeepImages is how many of the newest images each repository keeps.
	KeepImages int `json:"keep_images" gorm:"not null;default:0"`
	// MinAgeDays keeps anything younger, so a burst of builds is not pruned the same day.
	MinAgeDays int `json:"min_age_days" gorm:"not null;default:0"`
	// ReleaseWindow keeps the images of each app's last releases, so rollbacks keep working.
	ReleaseWindow int `json:"release_window" gorm:"not null;default:0"`
	// ProtectedTags are glob patterns; an image carrying a matching tag is never deleted.
	ProtectedTags []string  `json:"protected_tags" gorm:"serializer:json;type:text"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DefaultRegistryRetention is what an install starts with: everything set, nothing running.
func DefaultRegistryRetention() RegistryRetention {
	return RegistryRetention{KeepImages: 5, MinAgeDays: 7, ReleaseWindow: 5, ProtectedTags: []string{"latest"}}
}

// RegistryRetentionOverride lets a workspace opt out or keep more than the platform's count.
type RegistryRetentionOverride struct {
	WorkspaceID uint `json:"workspace_id" gorm:"primaryKey;autoIncrement:false"`
	Disabled    bool `json:"disabled" gorm:"not null;default:false"`
	// KeepImages 0 follows the platform.
	KeepImages int       `json:"keep_images" gorm:"not null;default:0"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type RegistryRetentionRunStatus string

const (
	RetentionRunRunning   RegistryRetentionRunStatus = "running"
	RetentionRunSucceeded RegistryRetentionRunStatus = "succeeded"
	RetentionRunFailed    RegistryRetentionRunStatus = "failed"
)

// RegistryRetentionRun is one preview or run, with its per-workspace report.
type RegistryRetentionRun struct {
	ID         uint                       `json:"id" gorm:"primaryKey"`
	DryRun     bool                       `json:"dry_run" gorm:"not null;default:false"`
	Trigger    string                     `json:"trigger" gorm:"not null"` // schedule | manual
	Status     RegistryRetentionRunStatus `json:"status" gorm:"not null;index"`
	Tags       int                        `json:"tags" gorm:"not null;default:0"`
	FreedBytes int64                      `json:"freed_bytes" gorm:"not null;default:0"`
	Error      string                     `json:"error,omitempty" gorm:"type:text"`
	Report     string                     `json:"-" gorm:"type:text"` // JSON []RetentionWorkspaceReport
	StartedAt  time.Time                  `json:"started_at"`
	FinishedAt *time.Time                 `json:"finished_at,omitempty"`
}
