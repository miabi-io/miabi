// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// FindByID looks up a domain by id alone (used by the DNS reconcile cron, which
// works from the record ledger and has no workspace in hand).
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

// ListPagedAll lists domains across every workspace for the platform-admin table,
// optionally filtered by a name search, a presentational status ("verified",
// "failed", "pending"), and a specific workspace. Newest first.
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

// ListVerifiedManual returns every verified domain that is NOT provider-automated
// (dns_provider_id is null). The drift cron re-checks these by hand; automated
// domains have their records reasserted by the DNS reconcile cron instead.
func (r *DomainRepository) ListVerifiedManual() ([]models.Domain, error) {
	var out []models.Domain
	err := r.db.Where("verified = ? AND banned = ? AND dns_provider_id IS NULL", true, false).Find(&out).Error
	return out, err
}

// ListVerifiedElsewhere returns verified, non-banned domains owned by workspaces other than the
// given one, enforcing that a hostname is verified by at most one workspace platform-wide. A
// banned domain is never served, so it doesn't block another workspace from claiming the name.
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
