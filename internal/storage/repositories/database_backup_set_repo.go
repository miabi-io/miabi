// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// DatabaseBackupSetRepository stores instance-level recovery points and reads
// them back with their items, which is the only way a set is useful.
type DatabaseBackupSetRepository struct{ db *gorm.DB }

func NewDatabaseBackupSetRepository(db *gorm.DB) *DatabaseBackupSetRepository {
	return &DatabaseBackupSetRepository{db: db}
}

func (r *DatabaseBackupSetRepository) Create(s *models.DatabaseBackupSet) error {
	return r.db.Create(s).Error
}

func (r *DatabaseBackupSetRepository) Update(s *models.DatabaseBackupSet) error {
	return r.db.Save(s).Error
}

// FindInWorkspace loads a set with its items, scoped to the workspace so a set id
// from another tenant reads as not found.
func (r *DatabaseBackupSetRepository) FindInWorkspace(workspaceID, id uint) (*models.DatabaseBackupSet, error) {
	var s models.DatabaseBackupSet
	err := r.db.Preload("Items", func(db *gorm.DB) *gorm.DB {
		return db.Order("backups.created_at DESC")
	}).Where("workspace_id = ? AND id = ?", workspaceID, id).First(&s).Error
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindByRef loads a set by its stable name, with items.
func (r *DatabaseBackupSetRepository) FindByRef(workspaceID uint, ref string) (*models.DatabaseBackupSet, error) {
	var s models.DatabaseBackupSet
	err := r.db.Preload("Items").Where("workspace_id = ? AND ref = ?", workspaceID, ref).First(&s).Error
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListByInstance returns an instance's recovery points newest-first, each with its
// items, so the history renders in one query pair rather than N+1.
func (r *DatabaseBackupSetRepository) ListByInstance(instanceID uint) ([]models.DatabaseBackupSet, error) {
	var sets []models.DatabaseBackupSet
	err := r.db.Preload("Items", func(db *gorm.DB) *gorm.DB {
		return db.Order("backups.id ASC")
	}).Where("instance_id = ?", instanceID).Order("created_at DESC").Find(&sets).Error
	return sets, err
}

// Delete removes the set row. Its items cascade at the database level; the
// service removes their artifacts first, which the database cannot do.
func (r *DatabaseBackupSetRepository) Delete(id uint) error {
	return r.db.Delete(&models.DatabaseBackupSet{}, id).Error
}

// FindByID loads a set without workspace scoping, for callers that already hold a
// workspace-scoped row pointing at it.
func (r *DatabaseBackupSetRepository) FindByID(id uint) (*models.DatabaseBackupSet, error) {
	var s models.DatabaseBackupSet
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSealed returns the workspace's sets that carry an envelope, which are the
// ones a passphrase rotation has to re-wrap.
func (r *DatabaseBackupSetRepository) ListSealed(workspaceID uint) ([]models.DatabaseBackupSet, error) {
	var out []models.DatabaseBackupSet
	err := r.db.Where("workspace_id = ? AND envelope <> ''", workspaceID).Find(&out).Error
	return out, err
}

// CreateSchedule stores a new instance-level set schedule.
func (r *DatabaseBackupSetRepository) CreateSchedule(s *models.DatabaseBackupSetSchedule) error {
	return r.db.Create(s).Error
}

func (r *DatabaseBackupSetRepository) UpdateSchedule(s *models.DatabaseBackupSetSchedule) error {
	return r.db.Save(s).Error
}

func (r *DatabaseBackupSetRepository) DeleteSchedule(id uint) error {
	return r.db.Delete(&models.DatabaseBackupSetSchedule{}, id).Error
}

// FindScheduleInWorkspace scopes the lookup so a schedule id from another tenant
// reads as not found.
func (r *DatabaseBackupSetRepository) FindScheduleInWorkspace(workspaceID, id uint) (*models.DatabaseBackupSetSchedule, error) {
	var s models.DatabaseBackupSetSchedule
	err := r.db.Where("workspace_id = ? AND id = ?", workspaceID, id).First(&s).Error
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *DatabaseBackupSetRepository) ListSchedulesByInstance(instanceID uint) ([]models.DatabaseBackupSetSchedule, error) {
	var out []models.DatabaseBackupSetSchedule
	err := r.db.Where("instance_id = ?", instanceID).Order("id ASC").Find(&out).Error
	return out, err
}

// ListEnabledSchedules feeds the cron manager at startup.
func (r *DatabaseBackupSetRepository) ListEnabledSchedules() ([]models.DatabaseBackupSetSchedule, error) {
	var out []models.DatabaseBackupSetSchedule
	err := r.db.Where("enabled = ?", true).Find(&out).Error
	return out, err
}
