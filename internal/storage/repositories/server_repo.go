// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"errors"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type ServerRepository struct {
	db *gorm.DB
}

func NewServerRepository(db *gorm.DB) *ServerRepository { return &ServerRepository{db: db} }

// CountWorkloads returns how many applications and database instances are placed
// on the node — the workloads a reachability change could disrupt. Soft-deleted
// rows are excluded by GORM's default scope.
func (r *ServerRepository) CountWorkloads(serverID uint) (apps int64, databases int64, err error) {
	if err = r.db.Model(&models.Application{}).Where("server_id = ?", serverID).Count(&apps).Error; err != nil {
		return 0, 0, err
	}
	if err = r.db.Model(&models.DatabaseInstance{}).Where("server_id = ?", serverID).Count(&databases).Error; err != nil {
		return 0, 0, err
	}
	return apps, databases, nil
}

func (r *ServerRepository) Create(s *models.Server) error { return r.db.Create(s).Error }
func (r *ServerRepository) Update(s *models.Server) error { return r.db.Save(s).Error }
func (r *ServerRepository) Delete(id uint) error {
	return r.db.Delete(&models.Server{}, id).Error
}

// FindBySlug resolves a node by its slug.
func (r *ServerRepository) FindBySlug(slug string) (*models.Server, error) {
	var s models.Server
	if err := r.db.Where("slug = ?", slug).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// FindByTokenHash resolves a remote node by its agent join-token hash.
func (r *ServerRepository) FindByTokenHash(hash string) (*models.Server, error) {
	var s models.Server
	if err := r.db.Where("token_hash = ? AND is_local = ?", hash, false).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *ServerRepository) List() ([]models.Server, error) {
	var servers []models.Server
	err := r.db.Order("id ASC").Find(&servers).Error
	return servers, err
}

func (r *ServerRepository) FindByID(id uint) (*models.Server, error) {
	var s models.Server
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// FindBySwarmNodeID resolves the Miabi node backing a swarm node id. It is how a
// self-registering agent is matched to an existing record instead of creating a
// duplicate: the agent reports the swarm node id its own engine holds, which is a
// stable identity the control plane can verify against its own `docker node ls`.
func (r *ServerRepository) FindBySwarmNodeID(swarmNodeID string) (*models.Server, error) {
	var s models.Server
	if err := r.db.Where("swarm_node_id = ?", swarmNodeID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateSwarmNodeID writes just that column.
//
// Deliberately not a full Save: a node's row is written from several places on a
// single agent connect (connected state, learned endpoint, swarm id) and by the
// cluster refresh loop. A read-modify-write of the whole row from each of them can
// silently clobber a field another just set. A column-scoped update cannot.
func (r *ServerRepository) UpdateSwarmNodeID(id uint, swarmNodeID string) error {
	return r.db.Model(&models.Server{}).
		Where("id = ?", id).
		Update("swarm_node_id", swarmNodeID).Error
}

func (r *ServerRepository) FindLocal() (*models.Server, error) {
	var s models.Server
	if err := r.db.Where("is_local = ?", true).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// EnsureLocal returns the local node, creating it if it does not yet exist.
func (r *ServerRepository) EnsureLocal(name, endpoint string) (*models.Server, error) {
	s, err := r.FindLocal()
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// Control-plane node defaults to manager + edge-gateway so it can run its own
	// Goma gateway for public ingress + TLS. Admin can switch back to port-forward.
	s = &models.Server{
		Name: name, Slug: name, DockerEndpoint: endpoint, IsLocal: true,
		Role: models.RoleManager, Connectivity: models.ConnectivityEdgeGateway,
		Status: models.ServerStatusUnknown,
	}
	if err := r.Create(s); err != nil {
		return nil, err
	}
	return s, nil
}

// IDByUID resolves a node's uid to its numeric id.
func (r *ServerRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.Server](r.db, uid)
}
