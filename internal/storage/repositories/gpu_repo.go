// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// ListByServer returns every discovered device on a node (enabled or not),
// ordered by host index for a stable admin listing.
func (r *GPUDeviceRepository) ListByServer(serverID uint) ([]models.GPUDevice, error) {
	var devices []models.GPUDevice
	err := r.db.Where("server_id = ?", serverID).Order("\"index\" ASC, id ASC").Find(&devices).Error
	return devices, err
}

// ListEnabledByServer returns only the admin-enabled devices on a node — the set
// an app may be scheduled onto.
func (r *GPUDeviceRepository) ListEnabledByServer(serverID uint) ([]models.GPUDevice, error) {
	var devices []models.GPUDevice
	err := r.db.Where("server_id = ? AND enabled = ?", serverID, true).
		Order("\"index\" ASC, id ASC").Find(&devices).Error
	return devices, err
}

// Upsert inserts a newly discovered device or refreshes an existing one (matched
// by UUID) with its latest hardware facts, stamping LastSeenAt. Admin policy
// flags (Enabled, Shared) are preserved on an existing row; a brand-new device
// arrives Enabled=false, Shared=true (fail closed — the admin opts each card in).
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
