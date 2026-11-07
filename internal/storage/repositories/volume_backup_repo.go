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
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type VolumeBackupRepository struct {
	db *gorm.DB
}

func NewVolumeBackupRepository(db *gorm.DB) *VolumeBackupRepository {
	return &VolumeBackupRepository{db: db}
}

func (r *VolumeBackupRepository) Create(b *models.VolumeBackup) error { return r.db.Create(b).Error }
func (r *VolumeBackupRepository) Update(b *models.VolumeBackup) error { return r.db.Save(b).Error }

// A zero ref is ignored so a store failure leaves the full DB tail intact.
func (r *VolumeBackupRepository) SetLogMeta(id uint, ref, tail string, bytes int64, lines int, truncated bool) error {
	if ref == "" {
		return nil
	}
	return r.db.Model(&models.VolumeBackup{}).Where("id = ?", id).
		Updates(map[string]any{
			"logs":          tail,
			"log_ref":       ref,
			"log_bytes":     bytes,
			"log_lines":     lines,
			"log_truncated": truncated,
		}).Error
}
func (r *VolumeBackupRepository) Delete(id uint) error {
	return r.db.Delete(&models.VolumeBackup{}, id).Error
}

func (r *VolumeBackupRepository) FindByID(id uint) (*models.VolumeBackup, error) {
	var b models.VolumeBackup
	if err := r.db.First(&b, id).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *VolumeBackupRepository) FindInWorkspace(workspaceID, id uint) (*models.VolumeBackup, error) {
	var b models.VolumeBackup
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&b).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *VolumeBackupRepository) ListByVolume(volumeID uint) ([]models.VolumeBackup, error) {
	var backups []models.VolumeBackup
	err := r.db.Where("volume_id = ?", volumeID).Order("created_at DESC").Find(&backups).Error
	return backups, err
}

// ListPointsByVolume returns a volume's recovery points (rows with a ref), newest first.
func (r *VolumeBackupRepository) ListPointsByVolume(volumeID uint) ([]models.VolumeBackup, error) {
	var out []models.VolumeBackup
	err := r.db.Where("volume_id = ? AND ref <> ''", volumeID).Order("created_at DESC").Find(&out).Error
	return out, err
}

// ListSealed returns points carrying an envelope, the ones a passphrase rotation must re-wrap.
func (r *VolumeBackupRepository) ListSealed(workspaceID uint) ([]models.VolumeBackup, error) {
	var out []models.VolumeBackup
	err := r.db.Where("workspace_id = ? AND envelope <> ''", workspaceID).Find(&out).Error
	return out, err
}

func (r *VolumeBackupRepository) CreateSchedule(s *models.VolumeBackupSchedule) error {
	return r.db.Create(s).Error
}

func (r *VolumeBackupRepository) UpdateSchedule(s *models.VolumeBackupSchedule) error {
	return r.db.Save(s).Error
}

func (r *VolumeBackupRepository) DeleteSchedule(id uint) error {
	return r.db.Delete(&models.VolumeBackupSchedule{}, id).Error
}

// Scoped so a schedule id from another tenant reads as not found.
func (r *VolumeBackupRepository) FindScheduleInWorkspace(workspaceID, id uint) (*models.VolumeBackupSchedule, error) {
	var s models.VolumeBackupSchedule
	if err := r.db.Where("workspace_id = ? AND id = ?", workspaceID, id).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *VolumeBackupRepository) FindScheduleByID(id uint) (*models.VolumeBackupSchedule, error) {
	var s models.VolumeBackupSchedule
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *VolumeBackupRepository) ListSchedulesByVolume(volumeID uint) ([]models.VolumeBackupSchedule, error) {
	var out []models.VolumeBackupSchedule
	err := r.db.Where("volume_id = ?", volumeID).Order("id ASC").Find(&out).Error
	return out, err
}

// ListEnabledSchedules feeds the cron manager at startup.
func (r *VolumeBackupRepository) ListEnabledSchedules() ([]models.VolumeBackupSchedule, error) {
	var out []models.VolumeBackupSchedule
	err := r.db.Where("enabled = ?", true).Find(&out).Error
	return out, err
}
