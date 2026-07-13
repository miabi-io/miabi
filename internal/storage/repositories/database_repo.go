// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// DatabaseRepository persists database server instances and the logical
// databases they host.
type DatabaseRepository struct {
	db *gorm.DB
}

func NewDatabaseRepository(db *gorm.DB) *DatabaseRepository { return &DatabaseRepository{db: db} }

// --- Instances ---

func (r *DatabaseRepository) Create(d *models.DatabaseInstance) error { return r.db.Create(d).Error }
func (r *DatabaseRepository) Update(d *models.DatabaseInstance) error { return r.db.Save(d).Error }

// RetargetNetwork repoints every instance pinned to the old Docker network at the
// new one. An instance stores its network by name (models.DatabaseInstance
// .NetworkName), so replacing the workspace network — as the bridge -> overlay
// migration does — must carry the instances across or their helper jobs would
// attach to a network that no longer exists.
func (r *DatabaseRepository) RetargetNetwork(oldName, newName string) error {
	return r.db.Model(&models.DatabaseInstance{}).
		Where("network_name = ?", oldName).
		Update("network_name", newName).Error
}

// Delete removes an instance and its logical database records (the server
// container holds the actual data, so dropping it removes those databases).
func (r *DatabaseRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Drop backups of this instance's logical databases first, so no backup row
		// orphans against a removed database_id.
		if err := tx.Where("database_id IN (?)",
			tx.Model(&models.Database{}).Select("id").Where("instance_id = ?", id),
		).Delete(&models.Backup{}).Error; err != nil {
			return err
		}
		if err := tx.Where("instance_id = ?", id).Delete(&models.Database{}).Error; err != nil {
			return err
		}
		// Clear the network associations (join rows) so the instance row is no
		// longer referenced by database_instance_networks.
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

// ExistsByID reports whether a non-deleted database instance exists. A
// soft-deleted instance reads as absent — the housekeeping "orphan" condition
// (deleted in Miabi but whose container still runs).
func (r *DatabaseRepository) ExistsByID(id uint) (bool, error) {
	var count int64
	err := r.db.Model(&models.DatabaseInstance{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

// --- Logical databases ---

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

// ListDatabasesByWorkspace returns every logical database in a workspace, across
// all instances. Used by the declarative reconcile to map manifest names to the
// exact logical database they own (via the declarative-name label).
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
