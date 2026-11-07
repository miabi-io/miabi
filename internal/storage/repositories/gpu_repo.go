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
	err := r.db.Where("server_id = ? AND enabled = ?", serverID, true).
		Order("\"index\" ASC, id ASC").Find(&devices).Error
	return devices, err
}

// Admin flags are preserved; a new device arrives Enabled=false, Shared=true (fail closed).
func (r *GPUDeviceRepository) Upsert(serverID uint, dev models.GPUDevice) error {
	now := time.Now()
	existing, err := r.FindByUUID(dev.UUID)
	if err == nil {
		existing.ServerID = serverID
		existing.Index = dev.Index
		existing.Vendor = dev.Vendor
		existing.Model = dev.Model
		existing.MemoryMB = dev.MemoryMB
		existing.LastSeenAt = &now
		return r.Update(existing)
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	dev.ServerID = serverID
	dev.Enabled = false
	dev.Shared = true
	dev.LastSeenAt = &now
	return r.Create(&dev)
}

// CountAll returns the total number of discovered devices (for metrics).
func (r *GPUDeviceRepository) CountAll() (int64, error) {
	var n int64
	err := r.db.Model(&models.GPUDevice{}).Count(&n).Error
	return n, err
}

// CountEnabled returns the number of admin-enabled devices (for metrics).
func (r *GPUDeviceRepository) CountEnabled() (int64, error) {
	var n int64
	err := r.db.Model(&models.GPUDevice{}).Where("enabled = ?", true).Count(&n).Error
	return n, err
}
