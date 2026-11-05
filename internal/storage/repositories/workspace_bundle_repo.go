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

// WorkspaceBundleRepository stores portable workspace bundle runs.
type WorkspaceBundleRepository struct {
	db *gorm.DB
}

func NewWorkspaceBundleRepository(db *gorm.DB) *WorkspaceBundleRepository {
	return &WorkspaceBundleRepository{db: db}
}

func (r *WorkspaceBundleRepository) Create(b *models.WorkspaceBundle) error {
	return r.db.Create(b).Error
}

func (r *WorkspaceBundleRepository) Update(b *models.WorkspaceBundle) error {
	return r.db.Save(b).Error
}

func (r *WorkspaceBundleRepository) Delete(id uint) error {
	return r.db.Delete(&models.WorkspaceBundle{}, id).Error
}

func (r *WorkspaceBundleRepository) FindByID(id uint) (*models.WorkspaceBundle, error) {
	var b models.WorkspaceBundle
	if err := r.db.First(&b, id).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *WorkspaceBundleRepository) FindInWorkspace(workspaceID, id uint) (*models.WorkspaceBundle, error) {
	var b models.WorkspaceBundle
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&b).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

// HasActive reports whether a run is pending or in flight for the workspace.
// Asked before starting one: two concurrent restores would race on every
// resource they create.
func (r *WorkspaceBundleRepository) HasActive(workspaceID uint) (bool, error) {
	var n int64
	err := r.db.Model(&models.WorkspaceBundle{}).
		Where("workspace_id = ? AND status IN ?", workspaceID,
			[]models.BackupStatus{models.BackupPending, models.BackupRunning}).
		Count(&n).Error
	return n > 0, err
}

// ListByWorkspace returns a workspace's bundle runs, most recent first.
func (r *WorkspaceBundleRepository) ListByWorkspace(workspaceID uint, limit int) ([]models.WorkspaceBundle, error) {
	var out []models.WorkspaceBundle
	q := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	return out, q.Find(&out).Error
}

// ListExpiredMigrationExports returns migration exports past their expiry whose staged data is still
// held. Purged ones are filtered after loading: the flag lives in a JSON column.
func (r *WorkspaceBundleRepository) ListExpiredMigrationExports(now time.Time) ([]models.WorkspaceBundle, error) {
	var rows []models.WorkspaceBundle
	if err := r.db.Where("kind = ? AND expires_at IS NOT NULL AND expires_at < ?", models.BundleMigrationExport, now).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, b := range rows {
		if b.Migration != nil && !b.Migration.Purged {
			out = append(out, b)
		}
	}
	return out, nil
}

// ListByKindForUser returns the runs of one kind a user started, most recent first.
func (r *WorkspaceBundleRepository) ListByKindForUser(kind models.WorkspaceBundleKind, userID uint, limit int) ([]models.WorkspaceBundle, error) {
	var out []models.WorkspaceBundle
	q := r.db.Where("kind = ? AND created_by = ?", kind, userID).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	return out, q.Find(&out).Error
}

// ListByKind returns every run of one kind, most recent first, for platform admins.
func (r *WorkspaceBundleRepository) ListByKind(kind models.WorkspaceBundleKind, limit int) ([]models.WorkspaceBundle, error) {
	var out []models.WorkspaceBundle
	q := r.db.Where("kind = ?", kind).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	return out, q.Find(&out).Error
}
