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

package repositories

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type RestoreRepository struct{ db *gorm.DB }

func NewRestoreRepository(db *gorm.DB) *RestoreRepository { return &RestoreRepository{db: db} }

func (r *RestoreRepository) Create(x *models.Restore) error { return r.db.Create(x).Error }

// The heartbeat is left to Touch, or a save would blank it and make a live restore look abandoned.
func (r *RestoreRepository) Update(x *models.Restore) error {
	return r.db.Omit("HeartbeatAt").Save(x).Error
}

func (r *RestoreRepository) Touch(id uint) error {
	return r.db.Model(&models.Restore{}).Where("id = ?", id).Update("heartbeat_at", time.Now()).Error
}

var activeStatuses = []models.BackupStatus{models.BackupPending, models.BackupRunning}

// Live is the restore still running against a target, judged by heartbeat; nil when none.
func (r *RestoreRepository) Live(target models.Restore, since time.Time) (*models.Restore, error) {
	q := r.db.Where("status IN ? AND COALESCE(heartbeat_at, started_at) >= ?", activeStatuses, since)
	switch {
	case target.VolumeID != 0:
		q = q.Where("volume_id = ?", target.VolumeID)
	case target.DatabaseID != 0:
		// A recovery point restore covers every database of its instance.
		q = q.Where("database_id = ? OR (kind = ? AND instance_id = ?)", target.DatabaseID, models.RestoreRecoveryPoint, target.InstanceID)
	default:
		q = q.Where("instance_id = ?", target.InstanceID)
	}
	var x models.Restore
	err := q.Order("id DESC").First(&x).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &x, nil
}

func (r *RestoreRepository) ListForInstance(workspaceID, instanceID uint, limit int) ([]models.Restore, error) {
	var out []models.Restore
	err := r.db.Where("workspace_id = ? AND instance_id = ?", workspaceID, instanceID).Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}

func (r *RestoreRepository) ListForVolume(workspaceID, volumeID uint, limit int) ([]models.Restore, error) {
	var out []models.Restore
	err := r.db.Where("workspace_id = ? AND volume_id = ?", workspaceID, volumeID).Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}

// FailStale closes restores whose process stopped beating before cutoff, such as one a restart abandoned.
func (r *RestoreRepository) FailStale(cutoff time.Time, reason string) (int64, error) {
	res := r.db.Model(&models.Restore{}).
		Where("status IN ? AND COALESCE(heartbeat_at, started_at) < ?", activeStatuses, cutoff).
		Updates(map[string]any{"status": models.BackupFailed, "error": reason, "finished_at": time.Now()})
	return res.RowsAffected, res.Error
}
