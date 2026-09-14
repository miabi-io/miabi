// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BrandAssetRepository struct {
	db *gorm.DB
}

func NewBrandAssetRepository(db *gorm.DB) *BrandAssetRepository { return &BrandAssetRepository{db: db} }

// Get returns one slot's asset, bytes included.
func (r *BrandAssetRepository) Get(slot string) (*models.BrandAsset, error) {
	var a models.BrandAsset
	if err := r.db.Where("slot = ?", slot).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// List returns every asset without its bytes: enough to build URLs on each sign-in
// page load without reading the images themselves.
func (r *BrandAssetRepository) List() ([]models.BrandAsset, error) {
	var out []models.BrandAsset
	err := r.db.Select("slot", "content_type", "sha256", "size", "updated_at").Order("slot ASC").Find(&out).Error
	return out, err
}

// Put stores an asset, replacing the slot's previous one.
func (r *BrandAssetRepository) Put(a *models.BrandAsset) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "slot"}},
		DoUpdates: clause.AssignmentColumns([]string{"content_type", "sha256", "size", "data", "updated_at"}),
	}).Create(a).Error
}

// Delete removes a slot's asset. An empty slot is not an error.
func (r *BrandAssetRepository) Delete(slot string) error {
	return r.db.Where("slot = ?", slot).Delete(&models.BrandAsset{}).Error
}
