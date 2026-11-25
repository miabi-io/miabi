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

type NetworkAllocationRepository struct {
	db *gorm.DB
}

func NewNetworkAllocationRepository(db *gorm.DB) *NetworkAllocationRepository {
	return &NetworkAllocationRepository{db: db}
}

// Unique indexes on docker_name and subnet make concurrent allocation fail for all but one caller.
func (r *NetworkAllocationRepository) Create(a *models.NetworkAllocation) error {
	return r.db.Create(a).Error
}

func (r *NetworkAllocationRepository) FindByDockerName(dockerName string) (*models.NetworkAllocation, error) {
	var a models.NetworkAllocation
	if err := r.db.Where("docker_name = ?", dockerName).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// DeleteByDockerName frees an allocation when its network is removed.
func (r *NetworkAllocationRepository) DeleteByDockerName(dockerName string) error {
	return r.db.Where("docker_name = ?", dockerName).Delete(&models.NetworkAllocation{}).Error
}

// ExistsBySubnet reports whether a subnet is already allocated or reserved.
func (r *NetworkAllocationRepository) ExistsBySubnet(subnet string) (bool, error) {
	var n int64
	err := r.db.Model(&models.NetworkAllocation{}).Where("subnet = ?", subnet).Count(&n).Error
	return n > 0, err
}

func (r *NetworkAllocationRepository) AllSubnets() ([]string, error) {
	var subnets []string
	err := r.db.Model(&models.NetworkAllocation{}).Pluck("subnet", &subnets).Error
	return subnets, err
}

// Reserved rows are included since they consume pool capacity.
func (r *NetworkAllocationRepository) Count() (int64, error) {
	var n int64
	err := r.db.Model(&models.NetworkAllocation{}).Count(&n).Error
	return n, err
}

// AllocationDetail is a ledger row with the workspace, network or stack, and node it belongs to.
type AllocationDetail struct {
	ID            uint      `json:"id"`
	DockerName    string    `json:"docker_name"`
	Subnet        string    `json:"subnet"`
	Gateway       string    `json:"gateway"`
	Kind          string    `json:"kind"`
	NodeID        uint      `json:"node_id"`
	NodeName      string    `json:"node_name,omitempty"`
	WorkspaceID   uint      `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name,omitempty"`
	OwnerName     string    `json:"owner_name,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListDetailed resolves names in one query; a row whose network or stack is gone keeps empty names.
func (r *NetworkAllocationRepository) ListDetailed() ([]AllocationDetail, error) {
	var out []AllocationDetail
	err := r.db.Raw(`
SELECT a.id, a.docker_name, a.subnet, a.gateway, a.kind, a.node_id, a.created_at,
	COALESCE(n.workspace_id, s.workspace_id, 0) AS workspace_id,
	COALESCE(NULLIF(w.display_name, ''), w.name, '') AS workspace_name,
	COALESCE(NULLIF(n.display_name, ''), n.name, NULLIF(s.display_name, ''), s.name, '') AS owner_name,
	COALESCE(NULLIF(sv.display_name, ''), sv.name, '') AS node_name
FROM network_allocations a
LEFT JOIN networks n ON n.docker_name = a.docker_name
LEFT JOIN stacks s ON s.docker_network = a.docker_name
LEFT JOIN workspaces w ON w.id = COALESCE(n.workspace_id, s.workspace_id)
LEFT JOIN servers sv ON sv.id = a.node_id AND a.node_id <> 0`).Scan(&out).Error
	return out, err
}

func (r *NetworkAllocationRepository) All() ([]models.NetworkAllocation, error) {
	var out []models.NetworkAllocation
	err := r.db.Find(&out).Error
	return out, err
}
