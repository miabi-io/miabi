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

const DatabaseBackupSetRefPrefix = "mbdb_"

// DatabaseBackupSet groups one run's per-database backups; any item failing fails the set, since a partial point is useless.
type DatabaseBackupSet struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index;not null"`
	InstanceID  uint `json:"instance_id" gorm:"index;not null"`

	Ref     string       `json:"ref" gorm:"uniqueIndex;not null"`
	Trigger string       `json:"trigger"` // manual | scheduled
	Status  BackupStatus `json:"status" gorm:"not null;default:pending"`

	// Recorded on the set as well as its items so a restore preflight asks once.
	Engine    DBEngine `json:"engine"`
	Version   string   `json:"version,omitempty"`
	Encrypted bool     `json:"encrypted" gorm:"not null;default:false"`
	// Seals the set's data key under the workspace passphrase so rotation re-seals bytes; never serialized (offline-attack target).
	Envelope string `json:"-" gorm:"type:text"`

	Destination string `json:"destination"` // local | s3
	S3Bucket    string `json:"s3_bucket,omitempty"`
	S3Path      string `json:"s3_path,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`

	Error string `json:"error,omitempty" gorm:"type:text"`

	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`

	// An empty VerifyStatus means never checked.
	VerifiedAt   *time.Time `json:"verified_at,omitempty"`
	VerifyStatus string     `json:"verify_status,omitempty"` // ok | failed
	VerifyError  string     `json:"verify_error,omitempty" gorm:"type:text"`

	// CASCADE: items have no meaning without their set, and leftovers would show half a recovery point.
	Items []Backup `json:"items,omitempty" gorm:"foreignKey:SetID;constraint:OnDelete:CASCADE"`
}

func NewDatabaseBackupSetRef(instance string, at time.Time) string {
	name := instance
	if name == "" {
		name = "unknown"
	}
	return DatabaseBackupSetRefPrefix + name + "_" + at.UTC().Format("20060102T150405Z")
}

// HasPinnedItem exempts the whole set from retention, since pruning around a pinned member leaves a partial point.
func (s *DatabaseBackupSet) HasPinnedItem() bool {
	for i := range s.Items {
		if s.Items[i].Pinned {
			return true
		}
	}
	return false
}

// Separate from BackupSchedule because that model requires a DatabaseID and its retention counts backups, not sets.
type DatabaseBackupSetSchedule struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index;not null"`
	InstanceID  uint   `json:"instance_id" gorm:"index;not null"`
	Cron        string `json:"cron" gorm:"not null"`
	Enabled     bool   `json:"enabled" gorm:"not null;default:true"`

	// 0 = unlimited; MaxSets counts recovery points, not the backups inside them.
	MaxSets       int `json:"max_sets" gorm:"not null;default:0"`
	RetentionDays int `json:"retention_days" gorm:"not null;default:0"`

	// 0 uses the conservative service default.
	Concurrency int `json:"concurrency" gorm:"not null;default:0"`

	// A run fails instead of writing a cleartext set while the workspace has no backup encryption.
	RequireEncryption bool `json:"require_encryption" gorm:"not null;default:false"`
	// ManagedBy is set when a manifest declared the schedule; the API refuses to delete it.
	ManagedBy string `json:"managed_by,omitempty"`
	// Declared is the manifest's schedule as written (a preset or cron), compared instead of the resolved Cron.
	Declared string `json:"declared,omitempty"`

	LastRunAt *time.Time `json:"last_run_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}
