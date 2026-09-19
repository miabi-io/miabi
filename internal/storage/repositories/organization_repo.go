// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// OrganizationRepository accesses tenant realms: the workspaces they own, their limits, and the
// identity configuration (SSO/enforced-login/SCIM) scoped to them.
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

// Create inserts an organization.
//
// MaxWorkspaces carries a column default of -1 (unlimited), which existing rows need at migration.
// GORM fills a zero-valued field from that default on insert, so an explicit "no workspaces allowed"
// (0) would silently become unlimited — the one value where the cap must not be guessed. Writing the
// column back afterwards is the least surprising fix; dropping the tag would give pre-existing
// organizations a cap of zero instead.
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

// List returns every organization, the default first, each annotated with how many workspaces it
// holds so the admin list does not need a query per row.
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

// SetDefault promotes one organization and demotes the rest in a single transaction, so no window
// exists in which two rows — or none — carry the flag that every nullable organization_id resolves to.
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

// ReassignWorkspaces moves every workspace of one organization to another. Used when an org is
// deleted, so its workspaces land back on the default rather than pointing at a row that is gone.
func (r *OrganizationRepository) ReassignWorkspaces(from, to uint) error {
	return r.db.Model(&models.Workspace{}).Where("organization_id = ?", from).
		Update("organization_id", to).Error
}

// ReassignUsers moves every user whose home realm is one organization to another.
func (r *OrganizationRepository) ReassignUsers(from, to uint) error {
	return r.db.Model(&models.User{}).Where("organization_id = ?", from).
		Update("organization_id", to).Error
}
