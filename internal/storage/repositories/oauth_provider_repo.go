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
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type OAuthProviderRepository struct {
	db *gorm.DB
}

func NewOAuthProviderRepository(db *gorm.DB) *OAuthProviderRepository {
	return &OAuthProviderRepository{db: db}
}

func (r *OAuthProviderRepository) Create(p *models.OAuthProvider) error {
	return r.db.Create(p).Error
}

func (r *OAuthProviderRepository) Update(p *models.OAuthProvider) error {
	return r.db.Save(p).Error
}

// Delete removes a provider with the identities bound to it, so a provider created later under a
// recycled id cannot inherit them.
func (r *OAuthProviderRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("provider_id = ?", id).Delete(&models.UserIdentity{}).Error; err != nil {
			return err
		}
		if err := tx.Where("provider_id = ?", id).Delete(&models.OAuthGroupMapping{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.OAuthProvider{}, id).Error
	})
}

// ListMappings returns a provider's group mappings in creation order.
func (r *OAuthProviderRepository) ListMappings(providerID uint) ([]models.OAuthGroupMapping, error) {
	var out []models.OAuthGroupMapping
	err := r.db.Where("provider_id = ?", providerID).Order("id").Find(&out).Error
	return out, err
}

// ReplaceMappings swaps a provider's group mappings for the given set in one transaction.
func (r *OAuthProviderRepository) ReplaceMappings(providerID uint, mappings []models.OAuthGroupMapping) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("provider_id = ?", providerID).Delete(&models.OAuthGroupMapping{}).Error; err != nil {
			return err
		}
		for i := range mappings {
			mappings[i].ID = 0
			mappings[i].ProviderID = providerID
			if err := tx.Create(&mappings[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Count returns the number of configured providers. Used by the single-provider
// quota guard (Community allows one; the multi_sso entitlement lifts the cap).
func (r *OAuthProviderRepository) Count() (int64, error) {
	var n int64
	if err := r.db.Model(&models.OAuthProvider{}).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// FindAll returns every provider (admin view), newest first.
func (r *OAuthProviderRepository) FindAll() ([]models.OAuthProvider, error) {
	var providers []models.OAuthProvider
	if err := r.db.Order("created_at DESC").Find(&providers).Error; err != nil {
		return nil, err
	}
	return providers, nil
}

// FindEnabled returns enabled, non-hidden providers for the login screen.
func (r *OAuthProviderRepository) FindEnabled() ([]models.OAuthProvider, error) {
	var providers []models.OAuthProvider
	if err := r.db.Where("enabled = ? AND hidden = ?", true, false).
		Order("name ASC").Find(&providers).Error; err != nil {
		return nil, err
	}
	return providers, nil
}

// HasEnabledHidden reports whether any enabled provider is hidden from the login
// buttons — i.e. reachable only via "Continue with SSO" email discovery. Drives
// whether that button is offered on the login screen.
func (r *OAuthProviderRepository) HasEnabledHidden() (bool, error) {
	var count int64
	if err := r.db.Model(&models.OAuthProvider{}).
		Where("enabled = ? AND hidden = ?", true, true).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *OAuthProviderRepository) FindEnabledByDomain(domain string) (*models.OAuthProvider, error) {
	target := strings.ToLower(strings.TrimSpace(domain))
	if target == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var providers []models.OAuthProvider
	if err := r.db.Where("enabled = ? AND allowed_domains <> ''", true).
		Order("name ASC").Find(&providers).Error; err != nil {
		return nil, err
	}
	for i := range providers {
		for _, d := range strings.Split(providers[i].AllowedDomains, ",") {
			if strings.ToLower(strings.TrimSpace(d)) == target {
				return &providers[i], nil
			}
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *OAuthProviderRepository) FindByID(id uint) (*models.OAuthProvider, error) {
	var p models.OAuthProvider
	if err := r.db.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *OAuthProviderRepository) FindByName(name string) (*models.OAuthProvider, error) {
	var p models.OAuthProvider
	if err := r.db.Where("name = ?", strings.ToLower(strings.TrimSpace(name))).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *OAuthProviderRepository) ExistsByName(name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.OAuthProvider{}).
		Where("name = ?", strings.ToLower(strings.TrimSpace(name))).Count(&count).Error
	return count > 0, err
}
