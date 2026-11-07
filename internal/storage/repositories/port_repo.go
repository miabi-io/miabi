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

type AppPortRepository struct {
	db *gorm.DB
}

func NewAppPortRepository(db *gorm.DB) *AppPortRepository { return &AppPortRepository{db: db} }

func (r *AppPortRepository) ListByApp(appID uint) ([]models.AppPort, error) {
	var ports []models.AppPort
	err := r.db.Where("application_id = ?", appID).Order("container_port ASC").Find(&ports).Error
	return ports, err
}

// ReplaceForApp swaps an application's declared container ports atomically.
func (r *AppPortRepository) ReplaceForApp(appID uint, ports []models.AppPort) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("application_id = ?", appID).Delete(&models.AppPort{}).Error; err != nil {
			return err
		}
		if len(ports) == 0 {
			return nil
		}
		for i := range ports {
			ports[i].ID = 0
			ports[i].ApplicationID = appID
		}
		return tx.Create(&ports).Error
	})
}

type PortBindingRepository struct {
	db *gorm.DB
}

func NewPortBindingRepository(db *gorm.DB) *PortBindingRepository {
	return &PortBindingRepository{db: db}
}

func (r *PortBindingRepository) Create(b *models.PortBinding) error { return r.db.Create(b).Error }
func (r *PortBindingRepository) Update(b *models.PortBinding) error { return r.db.Save(b).Error }
func (r *PortBindingRepository) Delete(id uint) error {
	return r.db.Delete(&models.PortBinding{}, id).Error
}

func (r *PortBindingRepository) FindByID(id uint) (*models.PortBinding, error) {
	var b models.PortBinding
	if err := r.db.First(&b, id).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *PortBindingRepository) FindInWorkspace(workspaceID, id uint) (*models.PortBinding, error) {
	var b models.PortBinding
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&b).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *PortBindingRepository) ListByApp(appID uint) ([]models.PortBinding, error) {
	var bindings []models.PortBinding
	err := r.db.Where("application_id = ?", appID).Order("created_at DESC").Find(&bindings).Error
	return bindings, err
}

func (r *PortBindingRepository) ListByWorkspace(workspaceID uint) ([]models.PortBinding, error) {
	var bindings []models.PortBinding
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&bindings).Error
	return bindings, err
}

// ListByStatus returns bindings across all workspaces in a given status (the admin review queue).
func (r *PortBindingRepository) ListByStatus(status models.PortBindingStatus) ([]models.PortBinding, error) {
	var bindings []models.PortBinding
	err := r.db.Where("status = ?", status).Order("created_at ASC").Find(&bindings).Error
	return bindings, err
}

// Rejected bindings are excluded: they claim nothing, so they are history rather than state.
func (r *PortBindingRepository) ListActive() ([]models.PortBinding, error) {
	var out []models.PortBinding
	err := r.db.Where("status IN ?", []models.PortBindingStatus{
		models.PortBindingPending, models.PortBindingApproved,
	}).Order("server_id ASC, host_port ASC").Find(&out).Error
	return out, err
}

func (r *PortBindingRepository) ListApprovedByApp(appID uint) ([]models.PortBinding, error) {
	var bindings []models.PortBinding
	err := r.db.Where("application_id = ? AND status = ?", appID, models.PortBindingApproved).Find(&bindings).Error
	return bindings, err
}

// DeleteByApp removes all of an app's bindings (used when the app is deleted).
func (r *PortBindingRepository) DeleteByApp(appID uint) error {
	return r.db.Where("application_id = ?", appID).Delete(&models.PortBinding{}).Error
}

// HostPortInUse reports whether an approved binding on the node already claims host:proto (host ports are per-node).
func (r *PortBindingRepository) HostPortInUse(serverID uint, hostPort int, protocol string, excludeID uint) (bool, error) {
	var count int64
	q := r.db.Model(&models.PortBinding{}).
		Where("server_id = ? AND host_port = ? AND protocol = ? AND status = ?", serverID, hostPort, protocol, models.PortBindingApproved)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	err := q.Count(&count).Error
	return count > 0, err
}
