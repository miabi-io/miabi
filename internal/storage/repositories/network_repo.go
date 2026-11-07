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

type NetworkRepository struct {
	db *gorm.DB
}

func NewNetworkRepository(db *gorm.DB) *NetworkRepository { return &NetworkRepository{db: db} }

func (r *NetworkRepository) Create(n *models.Network) error { return r.db.Create(n).Error }
func (r *NetworkRepository) Update(n *models.Network) error { return r.db.Save(n).Error }
func (r *NetworkRepository) Delete(id uint) error           { return r.db.Delete(&models.Network{}, id).Error }

// The ledger is authoritative after a restore, so recreating from it keeps names and addresses.
func (r *NetworkRepository) All() ([]models.Network, error) {
	var nets []models.Network
	err := r.db.Order("id ASC").Find(&nets).Error
	return nets, err
}

func (r *NetworkRepository) ListByDriver(driver string) ([]models.Network, error) {
	var nets []models.Network
	err := r.db.Where("driver = ?", driver).Order("workspace_id ASC, id ASC").Find(&nets).Error
	return nets, err
}

func (r *NetworkRepository) FindInWorkspace(workspaceID, id uint) (*models.Network, error) {
	var n models.Network
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&n).Error; err != nil {
		return nil, err
	}
	return &n, nil
}

func (r *NetworkRepository) ListByWorkspace(workspaceID uint) ([]models.Network, error) {
	var nets []models.Network
	err := r.db.Where("workspace_id = ?", workspaceID).Order("is_default DESC, created_at DESC").Find(&nets).Error
	return nets, err
}

// FindInWorkspaceByIDs returns the workspace's networks among the given ids.
func (r *NetworkRepository) FindInWorkspaceByIDs(workspaceID uint, ids []uint) ([]models.Network, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var nets []models.Network
	err := r.db.Where("workspace_id = ? AND id IN ?", workspaceID, ids).Find(&nets).Error
	return nets, err
}

func (r *NetworkRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Network{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

// DockerName is globally unique, which the import flow relies on for idempotency.
func (r *NetworkRepository) FindByDockerName(dockerName string) (*models.Network, error) {
	var n models.Network
	if err := r.db.Where("docker_name = ?", dockerName).First(&n).Error; err != nil {
		return nil, err
	}
	return &n, nil
}

// CountAppsUsing returns how many applications are attached to a network.
func (r *NetworkRepository) CountAppsUsing(networkID uint) (int64, error) {
	var count int64
	err := r.db.Table("application_networks").Where("network_id = ?", networkID).Count(&count).Error
	return count, err
}
