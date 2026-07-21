// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type VolumeRepository struct {
	db *gorm.DB
}

func NewVolumeRepository(db *gorm.DB) *VolumeRepository { return &VolumeRepository{db: db} }

func (r *VolumeRepository) Create(v *models.Volume) error { return r.db.Create(v).Error }
func (r *VolumeRepository) Update(v *models.Volume) error { return r.db.Save(v).Error }
func (r *VolumeRepository) Delete(id uint) error {
	// Drop the volume's backup records too, so none orphan against the removed volume_id.
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("volume_id = ?", id).Delete(&models.VolumeBackup{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Volume{}, id).Error
	})
}

func (r *VolumeRepository) FindInWorkspace(workspaceID, id uint) (*models.Volume, error) {
	var v models.Volume
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&v).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *VolumeRepository) ListByWorkspace(workspaceID uint) ([]models.Volume, error) {
	var vols []models.Volume
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&vols).Error
	return vols, err
}

// ServerIDsWithVolumes returns the distinct nodes owning a volume, so the usage
// sweep queries each once.
// ListAll returns every volume across all workspaces — used by the alerting
// disk-usage scanner (which compares UsedBytes against SizeBytes).
func (r *VolumeRepository) ListAll() ([]models.Volume, error) {
	var volumes []models.Volume
	err := r.db.Find(&volumes).Error
	return volumes, err
}

func (r *VolumeRepository) ServerIDsWithVolumes() ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.Volume{}).Distinct().Pluck("server_id", &ids).Error
	return ids, err
}

// SetUsage records a measured size by Docker name, touching only the two
// measurement columns. Unmeasured volumes keep their prior value.
func (r *VolumeRepository) SetUsage(dockerName string, usedBytes int64, at time.Time) error {
	return r.db.Model(&models.Volume{}).
		Where("docker_name = ?", dockerName).
		Updates(map[string]any{"used_bytes": usedBytes, "used_measured_at": at}).Error
}

// ExistsByID reports whether a (non-deleted) volume record with this id exists.
// A soft-deleted volume reads as absent — exactly the housekeeping "orphan"
// condition (a volume deleted in Miabi but still on the node).
func (r *VolumeRepository) ExistsByID(id uint) (bool, error) {
	var count int64
	err := r.db.Model(&models.Volume{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *VolumeRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Volume{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

// FindByDockerName returns the volume referencing the given Docker volume name,
// or gorm.ErrRecordNotFound. DockerName is globally unique, so no workspace
// scoping is needed (used by the import flow for idempotency).
func (r *VolumeRepository) FindByDockerName(dockerName string) (*models.Volume, error) {
	var v models.Volume
	if err := r.db.Where("docker_name = ?", dockerName).First(&v).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

// IDByUID resolves a volume's uid to its numeric id.
func (r *VolumeRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.Volume](r.db, uid)
}
