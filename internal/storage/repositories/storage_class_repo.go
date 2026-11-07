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

// Storage classes are platform-wide: they describe the operator's disks, and a plan decides who may use them.
type StorageClassRepository struct {
	db *gorm.DB
}

func NewStorageClassRepository(db *gorm.DB) *StorageClassRepository {
	return &StorageClassRepository{db: db}
}

func (r *StorageClassRepository) Create(c *models.StorageClass) error { return r.db.Create(c).Error }
func (r *StorageClassRepository) Update(c *models.StorageClass) error { return r.db.Save(c).Error }

func (r *StorageClassRepository) Delete(id uint) error {
	return r.db.Delete(&models.StorageClass{}, id).Error
}

func (r *StorageClassRepository) FindByID(id uint) (*models.StorageClass, error) {
	var c models.StorageClass
	if err := r.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *StorageClassRepository) FindByName(name string) (*models.StorageClass, error) {
	var c models.StorageClass
	if err := r.db.Where("name = ?", name).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *StorageClassRepository) List() ([]models.StorageClass, error) {
	var out []models.StorageClass
	err := r.db.Order("server_id ASC, name ASC").Find(&out).Error
	return out, err
}

// A Shared class is an operator assertion that the path exists identically on every node.
func (r *StorageClassRepository) ListByServer(serverID uint) ([]models.StorageClass, error) {
	var out []models.StorageClass
	// The builtin class is the engine's own volume directory, present on every node.
	err := r.db.Where("server_id = ? OR shared = ? OR builtin = ?", serverID, true, true).Order("name ASC").Find(&out).Error
	return out, err
}

// A disabled class is never returned, so disabling a default stops new volumes landing there.
func (r *StorageClassRepository) FindDefault(serverID uint) (*models.StorageClass, error) {
	var c models.StorageClass
	err := r.db.Where("is_default = ? AND enabled = ? AND (server_id = ? OR shared = ?)", true, true, serverID, true).
		Order("server_id DESC").First(&c).Error
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ClearDefault demotes the previous default in the same transaction as the promoting write.
func (r *StorageClassRepository) ClearDefault(serverID, exceptID uint) error {
	return r.db.Model(&models.StorageClass{}).
		Where("server_id = ? AND id <> ?", serverID, exceptID).
		Update("is_default", false).Error
}

func (r *StorageClassRepository) ExistsByName(name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.StorageClass{}).Where("name = ?", name).Count(&count).Error
	return count > 0, err
}

// Count includes the built-in class: an edition cap bounds the whole catalog.
func (r *StorageClassRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.StorageClass{}).Count(&count).Error
	return count, err
}

// CountVolumes reports how many volumes reference a class, so Delete can refuse one still in use.
func (r *StorageClassRepository) CountVolumes(name string) (int64, error) {
	var count int64
	err := r.db.Model(&models.Volume{}).Where("storage_class_name = ?", name).Count(&count).Error
	return count, err
}

// CountDatabases reports how many database instances keep their data on a class.
func (r *StorageClassRepository) CountDatabases(name string) (int64, error) {
	var count int64
	err := r.db.Model(&models.DatabaseInstance{}).Where("storage_class_name = ?", name).Count(&count).Error
	return count, err
}

// SetCapacity records a class's measured filesystem size, touching only the measurement columns.
func (r *StorageClassRepository) SetCapacity(id uint, capacityBytes, availableBytes int64, at time.Time) error {
	return r.db.Model(&models.StorageClass{}).Where("id = ?", id).
		Updates(map[string]any{
			"capacity_bytes":  capacityBytes,
			"available_bytes": availableBytes,
			"measured_at":     at,
		}).Error
}

// IDByUID resolves a storage class's uid to its numeric id.
func (r *StorageClassRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.StorageClass](r.db, uid)
}

// StorageClassUsage is one volume or database instance whose data lives on a class.
type StorageClassUsage struct {
	Kind          string     `json:"kind"`
	ID            uint       `json:"id"`
	UID           string     `json:"uid"`
	Name          string     `json:"name"`
	DisplayName   string     `json:"display_name"`
	WorkspaceID   uint       `json:"workspace_id"`
	WorkspaceName string     `json:"workspace_name"`
	ServerID      uint       `json:"server_id"`
	ServerName    string     `json:"server_name,omitempty" gorm:"-"`
	UsedBytes     int64      `json:"used_bytes"`
	MeasuredAt    *time.Time `json:"measured_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// Usage reads the sizes the usage sweeps last stored; it never measures.
func (r *StorageClassRepository) Usage(name string) ([]StorageClassUsage, error) {
	var out []StorageClassUsage
	err := r.db.Raw(`SELECT * FROM (
		SELECT 'volume' AS kind, v.id, v.uid, v.name, COALESCE(v.display_name, '') AS display_name, v.workspace_id, COALESCE(NULLIF(w.display_name, ''), w.name, '') AS workspace_name,
			v.server_id, v.used_bytes, v.used_measured_at AS measured_at, v.created_at
		FROM volumes v LEFT JOIN workspaces w ON w.id = v.workspace_id WHERE v.storage_class_name = ?
		UNION ALL
		SELECT 'database', d.id, d.uid, d.name, COALESCE(d.display_name, ''), d.workspace_id, COALESCE(NULLIF(w.display_name, ''), w.name, ''),
			d.server_id, COALESCE(d.size_bytes, 0), d.size_synced_at, d.created_at
		FROM database_instances d LEFT JOIN workspaces w ON w.id = d.workspace_id WHERE d.storage_class_name = ?
	) u ORDER BY used_bytes DESC, name`, name, name).Scan(&out).Error
	return out, err
}

func (r *StorageClassRepository) Servers() ([]models.Server, error) {
	var out []models.Server
	err := r.db.Order("id").Find(&out).Error
	return out, err
}
