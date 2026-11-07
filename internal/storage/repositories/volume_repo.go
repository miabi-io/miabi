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
		if err := tx.Where("volume_id = ?", id).Delete(&models.VolumeBackupSchedule{}).Error; err != nil {
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

// Touches only the measurement columns; unmeasured volumes keep their prior value.
func (r *VolumeRepository) SetUsage(dockerName string, usedBytes int64, at time.Time) error {
	return r.db.Model(&models.Volume{}).
		Where("docker_name = ?", dockerName).
		Updates(map[string]any{"used_bytes": usedBytes, "used_measured_at": at}).Error
}

// Keeps the first timestamp so a volume recreated by hand reads as replaced rather than adopting the empty one.
func (r *VolumeRepository) SetEngineCreatedAt(id uint, at string) error {
	if at == "" {
		return nil
	}
	return r.db.Model(&models.Volume{}).
		Where("id = ? AND (engine_created_at IS NULL OR engine_created_at = ?)", id, "").
		Update("engine_created_at", at).Error
}

// A soft-deleted volume reads as absent, the housekeeping "orphan" condition.
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

// DockerName is globally unique, so no workspace scoping is needed.
func (r *VolumeRepository) FindByDockerName(dockerName string) (*models.Volume, error) {
	var v models.Volume
	if err := r.db.Where("docker_name = ?", dockerName).First(&v).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

// Housekeeping uses it for volumes created before they carried io.miabi.volume.
func (r *VolumeRepository) ExistsByDockerName(dockerName string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Volume{}).Where("docker_name = ?", dockerName).Count(&count).Error
	return count > 0, err
}

// IDByUID resolves a volume's uid to its numeric id.
func (r *VolumeRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.Volume](r.db, uid)
}
