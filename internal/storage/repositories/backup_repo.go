// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type BackupRepository struct {
	db *gorm.DB
}

func NewBackupRepository(db *gorm.DB) *BackupRepository { return &BackupRepository{db: db} }

func (r *BackupRepository) Create(b *models.Backup) error { return r.db.Create(b).Error }
func (r *BackupRepository) Update(b *models.Backup) error { return r.db.Save(b).Error }

// SetLogMeta records the log-store reference + counters for a backup run and
// replaces the DB column with the bounded tail (the full log lives in the
// store). A zero ref is ignored so a store failure leaves the full DB tail intact.
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
