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

type BackupRepository struct {
	db *gorm.DB
}

func NewBackupRepository(db *gorm.DB) *BackupRepository { return &BackupRepository{db: db} }

func (r *BackupRepository) Create(b *models.Backup) error { return r.db.Create(b).Error }
// The heartbeat is left to Touch, or a mid-run save would blank it and make a live run look abandoned.
func (r *BackupRepository) Update(b *models.Backup) error {
	return r.db.Omit("HeartbeatAt").Save(b).Error
}

// Running reports a live single-database run, so a second click doesn't start another dump; an abandoned one doesn't count.
func (r *BackupRepository) Running(databaseID uint, since time.Time) (bool, error) {
	var n int64
	err := r.db.Model(&models.Backup{}).
		Where("database_id = ? AND set_id IS NULL AND status IN ?", databaseID, []models.BackupStatus{models.BackupPending, models.BackupRunning}).
		Where("COALESCE(heartbeat_at, started_at) >= ?", since).
		Count(&n).Error
	return n > 0, err
}

// Touch records that a run is still alive.
func (r *BackupRepository) Touch(id uint) error {
	return r.db.Model(&models.Backup{}).Where("id = ?", id).Update("heartbeat_at", time.Now()).Error
}

// FailStale closes runs whose process stopped beating before cutoff, such as one a restart abandoned.
func (r *BackupRepository) FailStale(cutoff time.Time, reason string) (int64, error) {
	res := r.db.Model(&models.Backup{}).
		Where("status IN ? AND COALESCE(heartbeat_at, started_at) < ?", []models.BackupStatus{models.BackupPending, models.BackupRunning}, cutoff).
		Updates(map[string]any{"status": models.BackupFailed, "error": reason, "finished_at": time.Now()})
	return res.RowsAffected, res.Error
}

// A zero ref is ignored so a store failure leaves the full DB tail intact.
func (r *BackupRepository) SetLogMeta(id uint, ref, tail string, bytes int64, lines int, truncated bool) error {
	if ref == "" {
		return nil
	}
	return r.db.Model(&models.Backup{}).Where("id = ?", id).
		Updates(map[string]any{
			"logs":          tail,
			"log_ref":       ref,
			"log_bytes":     bytes,
			"log_lines":     lines,
			"log_truncated": truncated,
		}).Error
}

func (r *BackupRepository) FindInWorkspace(workspaceID, id uint) (*models.Backup, error) {
	var b models.Backup
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&b).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *BackupRepository) Delete(id uint) error {
	return r.db.Delete(&models.Backup{}, id).Error
}

func (r *BackupRepository) ListByDatabase(databaseID uint) ([]models.Backup, error) {
	var backups []models.Backup
	err := r.db.Where("database_id = ?", databaseID).Order("created_at DESC").Find(&backups).Error
	return backups, err
}

func (r *BackupRepository) CreateSchedule(s *models.BackupSchedule) error {
	return r.db.Create(s).Error
}
func (r *BackupRepository) UpdateSchedule(s *models.BackupSchedule) error { return r.db.Save(s).Error }
func (r *BackupRepository) DeleteSchedule(id uint) error {
	return r.db.Delete(&models.BackupSchedule{}, id).Error
}

func (r *BackupRepository) FindScheduleInWorkspace(workspaceID, id uint) (*models.BackupSchedule, error) {
	var s models.BackupSchedule
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *BackupRepository) ListSchedulesByDatabase(databaseID uint) ([]models.BackupSchedule, error) {
	var schedules []models.BackupSchedule
	err := r.db.Where("database_id = ?", databaseID).Order("created_at DESC").Find(&schedules).Error
	return schedules, err
}

// ListEnabledSchedules returns all enabled schedules (for cron registration).
func (r *BackupRepository) ListEnabledSchedules() ([]models.BackupSchedule, error) {
	var schedules []models.BackupSchedule
	err := r.db.Where("enabled = ?", true).Find(&schedules).Error
	return schedules, err
}
