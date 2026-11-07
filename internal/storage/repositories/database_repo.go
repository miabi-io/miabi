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

type DatabaseRepository struct {
	db *gorm.DB
}

func NewDatabaseRepository(db *gorm.DB) *DatabaseRepository { return &DatabaseRepository{db: db} }

func (r *DatabaseRepository) Create(d *models.DatabaseInstance) error { return r.db.Create(d).Error }
func (r *DatabaseRepository) Update(d *models.DatabaseInstance) error { return r.db.Save(d).Error }

// Instances store their network by name, so replacing the workspace network must carry them across.
func (r *DatabaseRepository) RetargetNetwork(oldName, newName string) error {
	return r.db.Model(&models.DatabaseInstance{}).
		Where("network_name = ?", oldName).
		Update("network_name", newName).Error
}

// The server container holds the actual data, so dropping it removes those databases.
func (r *DatabaseRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Backups go first so no backup row orphans against a removed database_id.
		if err := tx.Where("database_id IN (?)",
			tx.Model(&models.Database{}).Select("id").Where("instance_id = ?", id),
		).Delete(&models.Backup{}).Error; err != nil {
			return err
		}
		if err := tx.Where("instance_id = ?", id).Delete(&models.Database{}).Error; err != nil {
			return err
		}
		if err := tx.Where("instance_id = ?", id).Delete(&models.DatabaseInstanceLink{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.DatabaseInstance{ID: id}).Association("Networks").Clear(); err != nil {
			return err
		}
		return tx.Delete(&models.DatabaseInstance{}, id).Error
	})
}

// FindByID loads an instance by id regardless of workspace (worker use).
func (r *DatabaseRepository) FindByID(id uint) (*models.DatabaseInstance, error) {
	var d models.DatabaseInstance
	if err := r.db.Preload("Networks").First(&d, id).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DatabaseRepository) FindInWorkspace(workspaceID, id uint) (*models.DatabaseInstance, error) {
	var d models.DatabaseInstance
	if err := r.db.Preload("Networks").Where("id = ? AND workspace_id = ?", id, workspaceID).First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// AddNetwork attaches a Docker network to the instance (idempotent).
func (r *DatabaseRepository) AddNetwork(inst *models.DatabaseInstance, net *models.Network) error {
	return r.db.Model(inst).Association("Networks").Append(net)
}

// RemoveNetwork detaches a Docker network from the instance.
func (r *DatabaseRepository) RemoveNetwork(inst *models.DatabaseInstance, net *models.Network) error {
	return r.db.Model(inst).Association("Networks").Delete(net)
}

func (r *DatabaseRepository) ListAllInstances() ([]models.DatabaseInstance, error) {
	var out []models.DatabaseInstance
	err := r.db.Preload("Networks").Order("workspace_id ASC, id ASC").Find(&out).Error
	return out, err
}

// Set only while empty, so a volume recreated by hand keeps the original timestamp and reads as replaced.
func (r *DatabaseRepository) SetVolumeEngineCreatedAt(id uint, at string) error {
	if at == "" {
		return nil
	}
	return r.db.Model(&models.DatabaseInstance{}).
		Where("id = ? AND (volume_engine_created_at IS NULL OR volume_engine_created_at = ?)", id, "").
		Update("volume_engine_created_at", at).Error
}

func (r *DatabaseRepository) ListByWorkspace(workspaceID uint) ([]models.DatabaseInstance, error) {
	var dbs []models.DatabaseInstance
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&dbs).Error
	return dbs, err
}

func (r *DatabaseRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.DatabaseInstance{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

// A soft-deleted instance reads as absent: the housekeeping orphan condition.
func (r *DatabaseRepository) ExistsByID(id uint) (bool, error) {
	var count int64
	err := r.db.Model(&models.DatabaseInstance{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *DatabaseRepository) CreateDatabase(d *models.Database) error { return r.db.Create(d).Error }
func (r *DatabaseRepository) UpdateDatabase(d *models.Database) error { return r.db.Save(d).Error }
func (r *DatabaseRepository) DeleteDatabase(id uint) error {
	return r.db.Delete(&models.Database{}, id).Error
}

func (r *DatabaseRepository) ListDatabases(instanceID uint) ([]models.Database, error) {
	var dbs []models.Database
	err := r.db.Where("instance_id = ?", instanceID).Order("created_at DESC").Find(&dbs).Error
	return dbs, err
}

func (r *DatabaseRepository) ListDatabasesByWorkspace(workspaceID uint) ([]models.Database, error) {
	var dbs []models.Database
	err := r.db.Where("workspace_id = ?", workspaceID).
		Order("created_at ASC").Find(&dbs).Error
	return dbs, err
}

// ListDatabasesByApp returns the logical databases attached to an application.
func (r *DatabaseRepository) ListDatabasesByApp(workspaceID, appID uint) ([]models.Database, error) {
	var dbs []models.Database
	err := r.db.Where("workspace_id = ? AND application_id = ?", workspaceID, appID).
		Order("created_at DESC").Find(&dbs).Error
	return dbs, err
}

// FindDatabaseInWorkspace loads a logical database scoped to a workspace.
func (r *DatabaseRepository) FindDatabaseInWorkspace(workspaceID, id uint) (*models.Database, error) {
	var d models.Database
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// FindDatabaseByID loads a logical database regardless of workspace (worker/cron).
func (r *DatabaseRepository) FindDatabaseByID(id uint) (*models.Database, error) {
	var d models.Database
	if err := r.db.First(&d, id).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DatabaseRepository) ExistsDatabaseByName(instanceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Database{}).
		Where("instance_id = ? AND name = ?", instanceID, name).Count(&count).Error
	return count > 0, err
}

// IDByUID resolves a database instance's uid to its numeric id.
func (r *DatabaseRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.DatabaseInstance](r.db, uid)
}

// Housekeeping must never reclaim the image a stopped instance needs to come back up.
func (r *DatabaseRepository) ImageRefs() ([]string, error) {
	var refs []string
	err := r.db.Model(&models.DatabaseInstance{}).
		Where("image <> ''").Pluck("image", &refs).Error
	return refs, err
}

// SaveInstanceLink creates or updates an app's link to an instance.
func (r *DatabaseRepository) SaveInstanceLink(l *models.DatabaseInstanceLink) error {
	return r.db.Save(l).Error
}

// FindInstanceLink loads an app's link to an instance.
func (r *DatabaseRepository) FindInstanceLink(workspaceID, instanceID, appID uint) (*models.DatabaseInstanceLink, error) {
	var l models.DatabaseInstanceLink
	err := r.db.Where("workspace_id = ? AND instance_id = ? AND application_id = ?", workspaceID, instanceID, appID).
		First(&l).Error
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// DeleteInstanceLink removes a link by id.
func (r *DatabaseRepository) DeleteInstanceLink(id uint) error {
	return r.db.Delete(&models.DatabaseInstanceLink{}, id).Error
}

// ListInstanceLinksByApp returns the instances linked to an application.
func (r *DatabaseRepository) ListInstanceLinksByApp(workspaceID, appID uint) ([]models.DatabaseInstanceLink, error) {
	var out []models.DatabaseInstanceLink
	err := r.db.Where("workspace_id = ? AND application_id = ?", workspaceID, appID).
		Order("created_at DESC").Find(&out).Error
	return out, err
}

// ListInstanceLinks returns every app link to an instance.
func (r *DatabaseRepository) ListInstanceLinks(instanceID uint) ([]models.DatabaseInstanceLink, error) {
	var out []models.DatabaseInstanceLink
	err := r.db.Where("instance_id = ?", instanceID).Order("created_at ASC").Find(&out).Error
	return out, err
}
