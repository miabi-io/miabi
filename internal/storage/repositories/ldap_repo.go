// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// LDAPRepository accesses LDAP/AD connections and their group mappings
// (Enterprise; empty in CE).
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

// FindEnabled returns the enabled configs (mappings preloaded), oldest-first, so
// the authenticator tries them in a stable order (first match wins).
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

// FindByName returns the config with the given name (mappings preloaded), for
// group reconciliation keyed on the identity's provider slug.
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
