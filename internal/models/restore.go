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

type RestoreKind string

const (
	RestoreDatabase      RestoreKind = "database"
	RestoreRecoveryPoint RestoreKind = "recovery_point"
	RestoreVolume        RestoreKind = "volume"
)

// Restore records a restore that runs after its request, so its progress and outcome outlive it.
type Restore struct {
	ID          uint        `json:"id" gorm:"primaryKey"`
	WorkspaceID uint        `json:"workspace_id" gorm:"index;not null"`
	Kind        RestoreKind `json:"kind" gorm:"not null"`
	// One target per kind: a logical database, a whole instance, or a volume.
	InstanceID uint `json:"instance_id,omitempty" gorm:"index;not null;default:0"`
	DatabaseID uint `json:"database_id,omitempty" gorm:"index;not null;default:0"`
	VolumeID   uint `json:"volume_id,omitempty" gorm:"index;not null;default:0"`
	// Source names what is restored, e.g. "backup #5" or a recovery point ref.
	Source   string       `json:"source"`
	SourceID uint         `json:"source_id"`
	Force    bool         `json:"force"`
	Status   BackupStatus `json:"status" gorm:"not null;default:running"`
	Error    string       `json:"error,omitempty" gorm:"type:text"`
	// Restored and Failed list per-database outcomes of a recovery point restore.
	Restored      []string   `json:"restored,omitempty" gorm:"serializer:json"`
	Failed        []string   `json:"failed,omitempty" gorm:"serializer:json"`
	RequestedByID *uint      `json:"requested_by_id,omitempty"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	HeartbeatAt   *time.Time `json:"-"`
	CreatedAt     time.Time  `json:"created_at"`
}
