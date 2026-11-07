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

type OrganizationRepository struct{ db *gorm.DB }

func NewOrganizationRepository(db *gorm.DB) *OrganizationRepository {
	return &OrganizationRepository{db: db}
}

// FindDefault returns the single default organization.
func (r *OrganizationRepository) FindDefault() (*models.Organization, error) {
	var org models.Organization
	if err := r.db.Where("is_default = ?", true).First(&org).Error; err != nil {
		return nil, err
	}
	return &org, nil
}

func (r *OrganizationRepository) FindByID(id uint) (*models.Organization, error) {
	var org models.Organization
	if err := r.db.First(&org, id).Error; err != nil {
		return nil, err
	}
	return &org, nil
}

func (r *OrganizationRepository) FindByName(name string) (*models.Organization, error) {
	var org models.Organization
	if err := r.db.Where("name = ?", name).First(&org).Error; err != nil {
		return nil, err
	}
	return &org, nil
}

// IDByUID resolves an organization's portable uid to its primary key.
func (r *OrganizationRepository) IDByUID(uid string) (uint, error) {
	var org models.Organization
	if err := r.db.Select("id").Where("uid = ?", uid).First(&org).Error; err != nil {
		return 0, err
	}
	return org.ID, nil
}

// GORM fills a zero field from the -1 column default on insert, so an explicit 0 is written back afterwards.
func (r *OrganizationRepository) Create(org *models.Organization) error {
	max := org.MaxWorkspaces
	if err := r.db.Create(org).Error; err != nil {
		return err
	}
	if org.MaxWorkspaces == max {
		return nil
	}
	org.MaxWorkspaces = max
	return r.db.Model(org).UpdateColumn("max_workspaces", max).Error
}

func (r *OrganizationRepository) Update(org *models.Organization) error {
	return r.db.Save(org).Error
}

func (r *OrganizationRepository) Delete(id uint) error {
	return r.db.Delete(&models.Organization{}, id).Error
}

func (r *OrganizationRepository) ExistsByName(name string) (bool, error) {
	var count int64
	if err := r.db.Model(&models.Organization{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// Annotated with workspace counts so the admin list needs no query per row.
func (r *OrganizationRepository) List() ([]models.Organization, error) {
	var orgs []models.Organization
	if err := r.db.Order("is_default DESC, name ASC").Find(&orgs).Error; err != nil {
		return nil, err
	}
	counts, err := r.workspaceCounts()
	if err != nil {
		return nil, err
	}
	for i := range orgs {
		orgs[i].WorkspaceCount = counts[orgs[i].ID]
	}
	return orgs, nil
}

func (r *OrganizationRepository) Labels(ids []uint) (map[uint]string, error) {
	out := map[uint]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID                uint
		Name, DisplayName string
	}
	if err := r.db.Model(&models.Organization{}).
		Select("id", "name", "display_name").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.DisplayName != "" {
			out[row.ID] = row.DisplayName
			continue
		}
		out[row.ID] = row.Name
	}
	return out, nil
}

func (r *OrganizationRepository) workspaceCounts() (map[uint]int64, error) {
	var rows []struct {
		OrganizationID uint
		N              int64
	}
	if err := r.db.Model(&models.Workspace{}).
		Select("organization_id, count(*) as n").
		Where("organization_id IS NOT NULL").
		Group("organization_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, row := range rows {
		out[row.OrganizationID] = row.N
	}
	return out, nil
}

// CountWorkspaces is how many workspaces the organization holds, for the cap check.
func (r *OrganizationRepository) CountWorkspaces(orgID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Workspace{}).Where("organization_id = ?", orgID).Count(&n).Error
	return n, err
}

// CountUsers is how many users call the organization home.
func (r *OrganizationRepository) CountUsers(orgID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.User{}).Where("organization_id = ?", orgID).Count(&n).Error
	return n, err
}

// Single transaction, so there is never a window where two rows, or none, carry the default flag.
func (r *OrganizationRepository) SetDefault(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Organization{}).Where("id <> ?", id).
			Update("is_default", false).Error; err != nil {
			return err
		}
		return tx.Model(&models.Organization{}).Where("id = ?", id).
			Update("is_default", true).Error
	})
}

func (r *OrganizationRepository) ReassignWorkspaces(from, to uint) error {
	return r.db.Model(&models.Workspace{}).Where("organization_id = ?", from).
		Update("organization_id", to).Error
}

// ReassignUsers moves every user whose home realm is one organization to another.
func (r *OrganizationRepository) ReassignUsers(from, to uint) error {
	return r.db.Model(&models.User{}).Where("organization_id = ?", from).
		Update("organization_id", to).Error
}
