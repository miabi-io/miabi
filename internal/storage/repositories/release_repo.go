// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type ReleaseRepository struct {
	db *gorm.DB
}

func NewReleaseRepository(db *gorm.DB) *ReleaseRepository { return &ReleaseRepository{db: db} }

func (r *ReleaseRepository) Create(rel *models.Release) error { return r.db.Create(rel).Error }

func (r *ReleaseRepository) FindByID(id uint) (*models.Release, error) {
	var rel models.Release
	if err := r.db.First(&rel, id).Error; err != nil {
		return nil, err
	}
	return &rel, nil
}

func (r *ReleaseRepository) ListByApp(appID uint) ([]models.Release, error) {
	var releases []models.Release
	err := r.db.Where("application_id = ?", appID).Order("version DESC").Find(&releases).Error
	return releases, err
}

// ListByWorkspace returns every release across the workspace's applications,
// newest first — the data behind the workspace-wide Releases screen.
func (r *ReleaseRepository) ListByWorkspace(workspaceID uint) ([]models.Release, error) {
	var releases []models.Release
	err := r.db.
		Joins("JOIN applications ON applications.id = releases.application_id").
		Where("applications.workspace_id = ?", workspaceID).
		Order("releases.created_at DESC").Find(&releases).Error
	return releases, err
}

// ListByWorkspacePaged returns a page of workspace releases plus the total
// count, newest first.
func (r *ReleaseRepository) ListByWorkspacePaged(workspaceID uint, limit, offset int) ([]models.Release, int64, error) {
	var (
		releases []models.Release
		total    int64
	)
	q := r.db.Model(&models.Release{}).
		Joins("JOIN applications ON applications.id = releases.application_id").
		Where("applications.workspace_id = ?", workspaceID)
	q.Count(&total)
	if err := q.Order("releases.created_at DESC").Limit(limit).Offset(offset).Find(&releases).Error; err != nil {
		return nil, 0, err
	}
	return releases, total, nil
}

func (r *ReleaseRepository) FindActive(appID uint) (*models.Release, error) {
	var rel models.Release
	if err := r.db.Where("application_id = ? AND active = ?", appID, true).First(&rel).Error; err != nil {
		return nil, err
	}
	return &rel, nil
}

// ListProtected returns releases that pin an image against GC: the active
// release of every app and any pinned (rollback-target) release. Their image
// IDs, digests, and refs must never be collected.
func (r *ReleaseRepository) ListProtected() ([]models.Release, error) {
	var out []models.Release
	err := r.db.Where("active = ? OR pinned = ?", true, true).Find(&out).Error
	return out, err
}

// FindByContainerID returns the release tracking the given Docker container ID,
// or gorm.ErrRecordNotFound. Used by the import flow to detect a container that
// is already adopted (idempotent re-scan/re-import).
func (r *ReleaseRepository) FindByContainerID(containerID string) (*models.Release, error) {
	var rel models.Release
	if err := r.db.Where("container_id = ?", containerID).First(&rel).Error; err != nil {
		return nil, err
	}
	return &rel, nil
}

func (r *ReleaseRepository) NextVersion(appID uint) (int, error) {
	var max int
	err := r.db.Model(&models.Release{}).Where("application_id = ?", appID).
		Select("COALESCE(MAX(version), 0)").Scan(&max).Error
	return max + 1, err
}

func (r *ReleaseRepository) Delete(id uint) error {
	return r.db.Delete(&models.Release{}, id).Error
}

func (r *ReleaseRepository) SetPinned(id uint, pinned bool) error {
	return r.db.Model(&models.Release{}).Where("id = ?", id).Update("pinned", pinned).Error
}

// Activate marks one release active and all others for the app inactive.
func (r *ReleaseRepository) Activate(appID, releaseID uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Release{}).Where("application_id = ?", appID).
			Update("active", false).Error; err != nil {
			return err
		}
		return tx.Model(&models.Release{}).Where("id = ?", releaseID).
			Update("active", true).Error
	})
}

// ImageRefs returns the image and digest of every release. These are the rollback targets, so
// housekeeping must never reclaim them however long they have sat without a container.
func (r *ReleaseRepository) ImageRefs() ([]string, error) {
	var rows []struct{ Image, Digest string }
	if err := r.db.Model(&models.Release{}).Select("image", "digest").Scan(&rows).Error; err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(rows)*2)
	for _, row := range rows {
		refs = append(refs, row.Image, row.Digest)
	}
	return refs, nil
}
