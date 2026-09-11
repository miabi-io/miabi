// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type ClusterRepository struct {
	db *gorm.DB
}

func NewClusterRepository(db *gorm.DB) *ClusterRepository { return &ClusterRepository{db: db} }

// List returns every cluster, the default first, with its node count.
func (r *ClusterRepository) List() ([]models.Cluster, error) {
	var out []models.Cluster
	if err := r.db.Order("is_default DESC, name ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	counts, err := r.nodeCounts()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].NodeCount = counts[out[i].ID]
	}
	return out, nil
}

func (r *ClusterRepository) nodeCounts() (map[uint]int64, error) {
	var rows []struct {
		ClusterID uint
		N         int64
	}
	if err := r.db.Model(&models.Server{}).Select("cluster_id, COUNT(*) AS n").Group("cluster_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, row := range rows {
		out[row.ClusterID] = row.N
	}
	return out, nil
}

// FindByID returns a cluster; id 0 resolves to the default cluster.
func (r *ClusterRepository) FindByID(id uint) (*models.Cluster, error) {
	if id == models.DefaultClusterID {
		return r.FindDefault()
	}
	var c models.Cluster
	if err := r.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *ClusterRepository) FindDefault() (*models.Cluster, error) {
	var c models.Cluster
	if err := r.db.Where("is_default = ?", true).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *ClusterRepository) FindByName(name string) (*models.Cluster, error) {
	var c models.Cluster
	if err := r.db.Where("name = ?", name).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// IDByUID resolves a cluster's uid to its numeric id.
func (r *ClusterRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.Cluster](r.db, uid)
}

// UpdateColumns writes only the given columns, so the refresh loop and an admin edit cannot clobber
// each other's fields.
func (r *ClusterRepository) UpdateColumns(id uint, cols map[string]any) error {
	return r.db.Model(&models.Cluster{}).Where("id = ?", id).Updates(cols).Error
}

// CreateStandalone gives a node that has no cluster yet a standalone cluster of its own.
func (r *ClusterRepository) CreateStandalone(srv *models.Server, name string) (*models.Cluster, error) {
	c := &models.Cluster{
		Name:            name,
		DisplayName:     srv.Label(),
		Mode:            models.ClusterModeStandalone,
		ManagerServerID: srv.ID,
		Visibility:      models.ClusterVisibilityAll,
		LegacyIngress:   srv.Connectivity == models.ConnectivityPortForward,
	}
	if srv.Connectivity == models.ConnectivityEdgeGateway {
		c.IngressServerID = srv.ID
	}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(c).Error; err != nil {
			return err
		}
		return assignServer(tx, srv.ID, c.ID)
	})
	if err != nil {
		return nil, err
	}
	srv.ClusterID = c.ID
	return c, nil
}

// AssignServer moves a node, and everything placed on it, into another cluster.
func (r *ClusterRepository) AssignServer(serverID, clusterID uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return assignServer(tx, serverID, clusterID)
	})
}

// DeleteIfEmpty removes a standalone cluster that no node belongs to any more.
func (r *ClusterRepository) DeleteIfEmpty(id uint) error {
	return deleteIfEmpty(r.db, id)
}

func assignServer(tx *gorm.DB, serverID, clusterID uint) error {
	var prev uint
	if err := tx.Model(&models.Server{}).Select("cluster_id").Where("id = ?", serverID).Scan(&prev).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.Server{}).Where("id = ?", serverID).Update("cluster_id", clusterID).Error; err != nil {
		return err
	}
	for _, m := range []any{&models.Application{}, &models.DatabaseInstance{}, &models.Volume{}, &models.Job{}} {
		if err := tx.Unscoped().Model(m).Where("server_id = ?", serverID).Update("cluster_id", clusterID).Error; err != nil {
			return err
		}
	}
	if prev == models.DefaultClusterID || prev == clusterID {
		return nil
	}
	if err := tx.Model(&models.Stack{}).Where("cluster_id = ?", prev).
		Where("NOT EXISTS (SELECT 1 FROM servers WHERE servers.cluster_id = ?)", prev).
		Update("cluster_id", clusterID).Error; err != nil {
		return err
	}
	return deleteIfEmpty(tx, prev)
}

func deleteIfEmpty(tx *gorm.DB, id uint) error {
	return tx.Where("id = ? AND is_default = ?", id, false).
		Where("NOT EXISTS (SELECT 1 FROM servers WHERE servers.cluster_id = ?)", id).
		Delete(&models.Cluster{}).Error
}
