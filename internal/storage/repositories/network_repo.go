// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// ListByDriver returns every workspace network provisioned with the given Docker
// driver, across all workspaces. Used by the bridge -> overlay migration that
// runs when cluster mode is enabled.
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

// FindByDockerName returns the network referencing the given Docker network
// name, or gorm.ErrRecordNotFound. DockerName is globally unique (used by the
// import flow for idempotency).
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
