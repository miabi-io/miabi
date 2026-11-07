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
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type LDAPRepository struct{ db *gorm.DB }

func NewLDAPRepository(db *gorm.DB) *LDAPRepository { return &LDAPRepository{db: db} }

func (r *LDAPRepository) Create(c *models.LDAPConfig) error { return r.db.Create(c).Error }
func (r *LDAPRepository) Update(c *models.LDAPConfig) error { return r.db.Save(c).Error }
func (r *LDAPRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("ldap_config_id = ?", id).Delete(&models.LDAPGroupMapping{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.LDAPConfig{}, id).Error
	})
}

// FindAll returns every config (mappings preloaded) newest-first, for the admin UI.
func (r *LDAPRepository) FindAll() ([]models.LDAPConfig, error) {
	var cfgs []models.LDAPConfig
	err := r.db.Preload("Mappings").Order("created_at DESC").Find(&cfgs).Error
	for i := range cfgs {
		cfgs[i].BindPasswordSet = cfgs[i].BindPasswordEnc != ""
	}
	return cfgs, err
}

// Oldest-first so the authenticator tries configs in a stable order (first match wins).
func (r *LDAPRepository) FindEnabled() ([]models.LDAPConfig, error) {
	var cfgs []models.LDAPConfig
	err := r.db.Preload("Mappings").Where("enabled = ?", true).Order("id ASC").Find(&cfgs).Error
	return cfgs, err
}

func (r *LDAPRepository) FindByID(id uint) (*models.LDAPConfig, error) {
	var cfg models.LDAPConfig
	if err := r.db.Preload("Mappings").First(&cfg, id).Error; err != nil {
		return nil, err
	}
	cfg.BindPasswordSet = cfg.BindPasswordEnc != ""
	return &cfg, nil
}

func (r *LDAPRepository) FindByName(name string) (*models.LDAPConfig, error) {
	var cfg models.LDAPConfig
	if err := r.db.Preload("Mappings").Where("name = ?", name).First(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (r *LDAPRepository) ExistsByName(name string) (bool, error) {
	var n int64
	err := r.db.Model(&models.LDAPConfig{}).Where("name = ?", name).Count(&n).Error
	return n > 0, err
}

func (r *LDAPRepository) CreateMapping(m *models.LDAPGroupMapping) error { return r.db.Create(m).Error }
func (r *LDAPRepository) DeleteMapping(configID, mappingID uint) error {
	return r.db.Where("id = ? AND ldap_config_id = ?", mappingID, configID).
		Delete(&models.LDAPGroupMapping{}).Error
}

func (r *LDAPRepository) Mappings(configID uint) ([]models.LDAPGroupMapping, error) {
	var ms []models.LDAPGroupMapping
	err := r.db.Where("ldap_config_id = ?", configID).Order("id ASC").Find(&ms).Error
	return ms, err
}
