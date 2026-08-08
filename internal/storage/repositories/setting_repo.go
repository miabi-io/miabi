// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SettingRepository struct {
	db *gorm.DB
}

func NewSettingRepository(db *gorm.DB) *SettingRepository { return &SettingRepository{db: db} }

// All returns every setting, ordered by key.
func (r *SettingRepository) All() ([]models.Setting, error) {
	var settings []models.Setting
	if err := r.db.Order("key ASC").Find(&settings).Error; err != nil {
		return nil, err
	}
	return settings, nil
}

// Get returns a single setting by key.
func (r *SettingRepository) Get(key string) (*models.Setting, error) {
	var s models.Setting
	if err := r.db.Where("key = ?", key).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// BulkUpsert inserts or updates the given settings by key in a single
// transaction. Value and Type are overwritten; CreatedAt is preserved.
func (r *SettingRepository) BulkUpsert(settings []models.Setting) error {
	if len(settings) == 0 {
		return nil
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "type", "updated_at"}),
	}).Create(&settings).Error
}

// CreateIfMissing inserts a setting only when its key does not yet exist. Used
// to seed defaults on first boot without clobbering admin changes.
func (r *SettingRepository) CreateIfMissing(s models.Setting) error {
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&s).Error
}

// Delete removes a setting by key. Absent keys are not an error, so callers
// clearing a marker (for example the post-restore quiesce flag) are idempotent.
func (r *SettingRepository) Delete(key string) error {
	return r.db.Where("key = ?", key).Delete(&models.Setting{}).Error
}
