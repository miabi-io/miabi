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

// GPUDeviceRepository stores the physical GPUs discovered on nodes.
type GPUDeviceRepository struct {
	db *gorm.DB
}

func NewGPUDeviceRepository(db *gorm.DB) *GPUDeviceRepository { return &GPUDeviceRepository{db: db} }

func (r *GPUDeviceRepository) Create(d *models.GPUDevice) error { return r.db.Create(d).Error }
func (r *GPUDeviceRepository) Update(d *models.GPUDevice) error { return r.db.Save(d).Error }

// FindByID resolves a device by its primary key.
func (r *GPUDeviceRepository) FindByID(id uint) (*models.GPUDevice, error) {
	var d models.GPUDevice
	if err := r.db.First(&d, id).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// FindByUUID resolves a device by its stable GPU UUID.
func (r *GPUDeviceRepository) FindByUUID(uuid string) (*models.GPUDevice, error) {
	var d models.GPUDevice
	if err := r.db.Where("uuid = ?", uuid).First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *GPUDeviceRepository) ListByServer(serverID uint) ([]models.GPUDevice, error) {
	var devices []models.GPUDevice
	err := r.db.Where("server_id = ?", serverID).Order("\"index\" ASC, id ASC").Find(&devices).Error
	return devices, err
}

func (r *GPUDeviceRepository) ListEnabledByServer(serverID uint) ([]models.GPUDevice, error) {
	var devices []models.GPUDevice
	err := r.db.Where("server_id = ? AND enabled = ? AND missing = ?", serverID, true, false).
		Order("\"index\" ASC, id ASC").Find(&devices).Error
	return devices, err
}

// Admin flags are preserved; a new device arrives Enabled=false, Shared=true (fail closed).
func (r *GPUDeviceRepository) Upsert(serverID uint, dev models.GPUDevice) error {
	now := time.Now()
	existing, err := r.FindByUUID(dev.UUID)
	if err == nil {
		// Column-targeted so a concurrent admin toggle of enabled/shared is never overwritten by a sweep.
		return r.db.Model(&models.GPUDevice{}).Where("id = ?", existing.ID).Updates(map[string]any{
			"server_id": serverID, "index": dev.Index, "vendor": dev.Vendor, "model": dev.Model,
			"memory_mb": dev.MemoryMB, "missing": false, "last_seen_at": now,
			"utilization_pct": dev.UtilizationPct, "memory_used_mb": dev.MemoryUsedMB, "sampled_at": now,
		}).Error
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	dev.ServerID = serverID
	dev.Enabled = false
	dev.Shared = true
	dev.Missing = false
	dev.LastSeenAt = &now
	dev.SampledAt = &now
	return r.Create(&dev)
}

func (r *GPUDeviceRepository) UpdateUsage(serverID uint, uuid string, utilizationPct, memoryUsedMB *int, at time.Time) error {
	return r.db.Model(&models.GPUDevice{}).Where("server_id = ? AND uuid = ?", serverID, uuid).Updates(map[string]any{
		"utilization_pct": utilizationPct, "memory_used_mb": memoryUsedMB, "sampled_at": at,
	}).Error
}

func (r *GPUDeviceRepository) ListAll() ([]models.GPUDevice, error) {
	var devices []models.GPUDevice
	err := r.db.Order("server_id ASC, \"index\" ASC, id ASC").Find(&devices).Error
	return devices, err
}

// ServersWithEnabled lists the nodes that have at least one enabled, present card.
func (r *GPUDeviceRepository) ServersWithEnabled() ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.GPUDevice{}).Where("enabled = ? AND missing = ?", true, false).
		Distinct("server_id").Pluck("server_id", &ids).Error
	return ids, err
}

// MarkMissing flags the node's devices whose UUID is not in seen, and returns how many changed.
func (r *GPUDeviceRepository) MarkMissing(serverID uint, seen []string) (int64, error) {
	q := r.db.Model(&models.GPUDevice{}).Where("server_id = ? AND missing = ?", serverID, false)
	if len(seen) > 0 {
		q = q.Where("uuid NOT IN ?", seen)
	}
	res := q.Update("missing", true)
	return res.RowsAffected, res.Error
}
