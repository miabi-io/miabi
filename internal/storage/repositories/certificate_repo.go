// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// CertificateRepository persists workspace-scoped imported TLS certificates.
type CertificateRepository struct {
	db *gorm.DB
}

func NewCertificateRepository(db *gorm.DB) *CertificateRepository {
	return &CertificateRepository{db: db}
}

func (r *CertificateRepository) Create(cert *models.Certificate) error {
	return r.db.Create(cert).Error
}
func (r *CertificateRepository) Update(cert *models.Certificate) error { return r.db.Save(cert).Error }

func (r *CertificateRepository) FindInWorkspace(workspaceID, id uint) (*models.Certificate, error) {
	var c models.Certificate
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *CertificateRepository) ListByWorkspace(workspaceID uint) ([]models.Certificate, error) {
	var certs []models.Certificate
	err := r.db.Where("workspace_id = ?", workspaceID).Order("name ASC").Find(&certs).Error
	return certs, err
}

// FindByName returns a workspace's certificate by name, or ErrRecordNotFound.
func (r *CertificateRepository) FindByName(workspaceID uint, name string) (*models.Certificate, error) {
	var c models.Certificate
	if err := r.db.Where("workspace_id = ? AND name = ?", workspaceID, name).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// ListManagedExpiring returns ACME-managed, auto-renew certificates whose NotAfter
// falls before the cutoff (across workspaces) — used by the renewal cron.
func (r *CertificateRepository) ListManagedExpiring(cutoff time.Time) ([]models.Certificate, error) {
	var certs []models.Certificate
	err := r.db.Where("source = ? AND auto_renew = ? AND not_after < ?", models.CertSourceACME, true, cutoff).
		Order("not_after ASC").Find(&certs).Error
	return certs, err
}

func (r *CertificateRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Certificate{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

func (r *CertificateRepository) Delete(id uint) error {
	return r.db.Delete(&models.Certificate{}, id).Error
}

// ListExpiringBefore returns certificates whose NotAfter falls before the cutoff
// (across all workspaces) — used by the expiry-monitor cron.
func (r *CertificateRepository) ListExpiringBefore(cutoff time.Time) ([]models.Certificate, error) {
	var certs []models.Certificate
	err := r.db.Where("not_after < ?", cutoff).Order("not_after ASC").Find(&certs).Error
	return certs, err
}

// ListByStatus returns certificates in a given status (e.g. "failed") across all
// workspaces — used by the alerting scanner.
func (r *CertificateRepository) ListByStatus(status string) ([]models.Certificate, error) {
	var certs []models.Certificate
	err := r.db.Where("status = ?", status).Order("updated_at DESC").Find(&certs).Error
	return certs, err
}
