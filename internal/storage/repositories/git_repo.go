// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type GitRepoRepository struct {
	db *gorm.DB
}

func NewGitRepoRepository(db *gorm.DB) *GitRepoRepository { return &GitRepoRepository{db: db} }

func (r *GitRepoRepository) Create(g *models.GitRepository) error { return r.db.Create(g).Error }
func (r *GitRepoRepository) Update(g *models.GitRepository) error { return r.db.Save(g).Error }

// SetConnection records the outcome of a reachability check. A targeted update
// rather than a Save: the caller's struct may have been stripped of its secret
// for a response, and Save would write that emptiness back.
func (r *GitRepoRepository) SetConnection(id uint, status models.GitConnectionStatus, reason string, at time.Time) error {
	return r.db.Model(&models.GitRepository{}).Where("id = ?", id).
		Updates(map[string]any{
			"connection_status":     status,
			"connection_error":      reason,
			"connection_checked_at": at,
		}).Error
}
func (r *GitRepoRepository) Delete(id uint) error {
	return r.db.Delete(&models.GitRepository{}, id).Error
}

func (r *GitRepoRepository) FindInWorkspace(workspaceID, id uint) (*models.GitRepository, error) {
	var g models.GitRepository
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&g).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

func (r *GitRepoRepository) ListByWorkspace(workspaceID uint) ([]models.GitRepository, error) {
	var repos []models.GitRepository
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&repos).Error
	return repos, err
}

func (r *GitRepoRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.GitRepository{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}
