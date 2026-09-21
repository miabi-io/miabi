// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// RunnerRepository persists build/pipeline runners. Workspace-owned runners
// carry a WorkspaceID; platform-shared runners have a nil WorkspaceID.
type RunnerRepository struct {
	db *gorm.DB
}

func NewRunnerRepository(db *gorm.DB) *RunnerRepository { return &RunnerRepository{db: db} }

func (r *RunnerRepository) Create(m *models.Runner) error { return r.db.Create(m).Error }
func (r *RunnerRepository) Update(m *models.Runner) error { return r.db.Save(m).Error }

func (r *RunnerRepository) Delete(id uint) error {
	return r.db.Delete(&models.Runner{}, id).Error
}

// FindByID resolves a runner by primary key (any scope).
func (r *RunnerRepository) FindByID(id uint) (*models.Runner, error) {
	var m models.Runner
	if err := r.db.First(&m, id).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindInWorkspace resolves a runner owned by the given workspace, so a caller
// can only reach its own runners (a shared runner is invisible here).
func (r *RunnerRepository) FindInWorkspace(workspaceID, id uint) (*models.Runner, error) {
	var m models.Runner
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindShared resolves a platform-shared runner (nil workspace) by id.
func (r *RunnerRepository) FindShared(id uint) (*models.Runner, error) {
	var m models.Runner
	if err := r.db.Where("id = ? AND workspace_id IS NULL", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindByTokenHash resolves a runner by its registration-token hash (any scope).
func (r *RunnerRepository) FindByTokenHash(hash string) (*models.Runner, error) {
	var m models.Runner
	if err := r.db.Where("token_hash = ?", hash).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// ListByWorkspace returns a workspace's own runners, newest first.
func (r *RunnerRepository) ListByWorkspace(workspaceID uint) ([]models.Runner, error) {
	var out []models.Runner
	err := r.db.Where("workspace_id = ?", workspaceID).Order("id DESC").Find(&out).Error
	return out, err
}

// ListShared returns all platform-shared runners (nil workspace), newest first.
func (r *RunnerRepository) ListShared() ([]models.Runner, error) {
	var out []models.Runner
	err := r.db.Where("workspace_id IS NULL").Order("id DESC").Find(&out).Error
	return out, err
}

// CountShared reports the size of the platform-shared pool, for the edition cap.
func (r *RunnerRepository) CountShared() (int64, error) {
	var count int64
	err := r.db.Model(&models.Runner{}).Where("workspace_id IS NULL").Count(&count).Error
	return count, err
}

// ListAll returns every runner across all scopes — the alert scanner's view,
// which has to reason about reachability platform-wide rather than per workspace.
func (r *RunnerRepository) ListAll() ([]models.Runner, error) {
	var out []models.Runner
	err := r.db.Order("id DESC").Find(&out).Error
	return out, err
}

// ListSchedulable returns the runners a workspace's jobs may run on: its own
// runners plus (when includeShared) the platform-shared pool. Used by the
// scheduler and the "waiting for a runner" surface.
func (r *RunnerRepository) ListSchedulable(workspaceID uint, includeShared bool) ([]models.Runner, error) {
	var out []models.Runner
	q := r.db.Where("workspace_id = ?", workspaceID)
	if includeShared {
		q = r.db.Where("workspace_id = ? OR workspace_id IS NULL", workspaceID)
	}
	err := q.Order("id DESC").Find(&out).Error
	return out, err
}

// ExistsByName reports whether a runner with the given name already exists in
// the same scope (workspaceID nil = the shared pool), excluding excludeID.
func (r *RunnerRepository) ExistsByName(workspaceID *uint, name string, excludeID uint) (bool, error) {
	q := r.db.Model(&models.Runner{}).Where("name = ? AND id <> ?", name, excludeID)
	if workspaceID == nil {
		q = q.Where("workspace_id IS NULL")
	} else {
		q = q.Where("workspace_id = ?", *workspaceID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
