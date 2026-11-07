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
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// LocationMigrationRepository stores location migrations.
type LocationMigrationRepository struct {
	db *gorm.DB
}

func NewLocationMigrationRepository(db *gorm.DB) *LocationMigrationRepository {
	return &LocationMigrationRepository{db: db}
}

// DB exposes the handle so a caller can run the placement switch in one transaction.
func (r *LocationMigrationRepository) DB() *gorm.DB { return r.db }

func (r *LocationMigrationRepository) Create(m *models.LocationMigration) error {
	return r.db.Create(m).Error
}

// Request flags and lease are omitted so a progress save can't erase a cancel that arrived meanwhile.
func (r *LocationMigrationRepository) Update(m *models.LocationMigration) error {
	return r.db.Omit("cutover_requested", "cancel_requested", "lease_until").Save(m).Error
}

func (r *LocationMigrationRepository) FindByID(id uint) (*models.LocationMigration, error) {
	var m models.LocationMigration
	if err := r.db.First(&m, id).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *LocationMigrationRepository) FindInWorkspace(workspaceID, id uint) (*models.LocationMigration, error) {
	var m models.LocationMigration
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

var openStatuses = []models.MigrationStatus{
	models.MigrationRunning, models.MigrationAwaitingCutover, models.MigrationCutOver,
}

// A cut-over migration still counts: its source copy is what a rollback needs.
func (r *LocationMigrationRepository) ActiveForApp(appID uint) (*models.LocationMigration, error) {
	var m models.LocationMigration
	err := r.db.Where("application_id = ? AND status IN ?", appID, openStatuses).
		Order("id DESC").First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ListByWorkspace returns a workspace's migrations, newest first.
func (r *LocationMigrationRepository) ListByWorkspace(workspaceID uint, limit int) ([]models.LocationMigration, error) {
	var out []models.LocationMigration
	q := r.db.Where("workspace_id = ?", workspaceID).Order("id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	return out, q.Find(&out).Error
}

// ListByApp returns an application's migrations, newest first.
func (r *LocationMigrationRepository) ListByApp(appID uint, limit int) ([]models.LocationMigration, error) {
	var out []models.LocationMigration
	q := r.db.Where("application_id = ?", appID).Order("id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	return out, q.Find(&out).Error
}

// ListAll returns every migration, newest first, for the platform view.
func (r *LocationMigrationRepository) ListAll(limit int) ([]models.LocationMigration, error) {
	var out []models.LocationMigration
	q := r.db.Order("id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	return out, q.Find(&out).Error
}

// ListOrphaned returns running migrations whose worker lease lapsed: the process running them died.
func (r *LocationMigrationRepository) ListOrphaned(now time.Time) ([]models.LocationMigration, error) {
	var out []models.LocationMigration
	err := r.db.Where("status = ? AND (lease_until IS NULL OR lease_until < ?)", models.MigrationRunning, now).
		Find(&out).Error
	return out, err
}

// ListDueForFinalize returns cut-over migrations whose grace period ended.
func (r *LocationMigrationRepository) ListDueForFinalize(now time.Time) ([]models.LocationMigration, error) {
	var out []models.LocationMigration
	err := r.db.Where("status = ? AND finalize_after IS NOT NULL AND finalize_after < ?", models.MigrationCutOver, now).
		Find(&out).Error
	return out, err
}

// Guards against two workers running one migration after a resume races a slow original.
func (r *LocationMigrationRepository) AcquireLease(id uint, now, until time.Time) (bool, error) {
	res := r.db.Model(&models.LocationMigration{}).
		Where("id = ? AND (lease_until IS NULL OR lease_until < ?)", id, now).
		Update("lease_until", until)
	return res.RowsAffected == 1, res.Error
}

// RenewLease extends a lease this worker holds.
func (r *LocationMigrationRepository) RenewLease(id uint, until time.Time) error {
	return r.db.Model(&models.LocationMigration{}).Where("id = ?", id).Update("lease_until", until).Error
}

// ReleaseLease drops the lease so a later request (cutover, rollback) can run at once.
func (r *LocationMigrationRepository) ReleaseLease(id uint) error {
	return r.db.Model(&models.LocationMigration{}).Where("id = ?", id).Update("lease_until", nil).Error
}

// SetFlag writes one request flag without touching the rest of the row a worker may be saving.
func (r *LocationMigrationRepository) SetFlag(id uint, column string, value bool) error {
	return r.db.Model(&models.LocationMigration{}).Where("id = ?", id).Update(column, value).Error
}

// Flags re-reads the request flags a running worker polls.
func (r *LocationMigrationRepository) Flags(id uint) (cutover, cancel bool, err error) {
	var m models.LocationMigration
	err = r.db.Select("cutover_requested", "cancel_requested").First(&m, id).Error
	return m.CutoverRequested, m.CancelRequested, err
}
