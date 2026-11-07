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

// DomainRepository persists owned domains.
type DomainRepository struct {
	db *gorm.DB
}

func NewDomainRepository(db *gorm.DB) *DomainRepository { return &DomainRepository{db: db} }

func (r *DomainRepository) Create(d *models.Domain) error { return r.db.Create(d).Error }
func (r *DomainRepository) Update(d *models.Domain) error { return r.db.Save(d).Error }
func (r *DomainRepository) Delete(workspaceID, id uint) error {
	return r.db.Where("workspace_id = ?", workspaceID).Delete(&models.Domain{}, id).Error
}

func (r *DomainRepository) FindInWorkspace(workspaceID, id uint) (*models.Domain, error) {
	var d models.Domain
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// Unscoped: the DNS reconcile cron works from the record ledger with no workspace in hand.
func (r *DomainRepository) FindByID(id uint) (*models.Domain, error) {
	var d models.Domain
	if err := r.db.First(&d, id).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DomainRepository) ListByWorkspace(workspaceID uint) ([]models.Domain, error) {
	var out []models.Domain
	err := r.db.Where("workspace_id = ?", workspaceID).Order("name ASC").Find(&out).Error
	return out, err
}

func (r *DomainRepository) ListPagedAll(search, status string, workspaceID *uint, limit, offset int) ([]models.Domain, int64, error) {
	q := r.db.Model(&models.Domain{})
	if s := strings.TrimSpace(search); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ?", like)
	}
	switch models.DomainStatus(strings.TrimSpace(status)) {
	case models.DomainStatusBanned:
		q = q.Where("banned = ?", true)
	case models.DomainStatusVerified:
		q = q.Where("banned = ? AND verified = ?", false, true)
	case models.DomainStatusFailed:
		q = q.Where("banned = ? AND verified = ? AND verification_error <> ?", false, false, "")
	case models.DomainStatusPending:
		q = q.Where("banned = ? AND verified = ? AND (verification_error = ? OR verification_error IS NULL)", false, false, "")
	}
	if workspaceID != nil {
		q = q.Where("workspace_id = ?", *workspaceID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []models.Domain
	err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&out).Error
	return out, total, err
}

// Automated domains have their records reasserted by the DNS reconcile cron instead.
func (r *DomainRepository) ListVerifiedManual() ([]models.Domain, error) {
	var out []models.Domain
	err := r.db.Where("verified = ? AND banned = ? AND dns_provider_id IS NULL", true, false).Find(&out).Error
	return out, err
}

// A banned domain is never served, so it doesn't block another workspace from claiming the name.
func (r *DomainRepository) ListVerifiedElsewhere(excludeWorkspaceID uint) ([]models.Domain, error) {
	var out []models.Domain
	err := r.db.Where("verified = ? AND banned = ? AND workspace_id <> ?", true, false, excludeWorkspaceID).
		Find(&out).Error
	return out, err
}

func (r *DomainRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Domain{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}
