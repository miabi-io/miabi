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
	"errors"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type ServerRepository struct {
	db *gorm.DB
}

func NewServerRepository(db *gorm.DB) *ServerRepository { return &ServerRepository{db: db} }

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

func (r *ServerRepository) Update(s *models.Server) error {
	return r.db.Omit("GatewayRedisPasswordEnc").Save(s).Error
}

func (r *ServerRepository) ClaimGatewayRedisPassword(id uint, enc string, replace bool) (bool, error) {
	q := r.db.Model(&models.Server{}).Where("id = ?", id)
	if !replace {
		q = q.Where("gateway_redis_password_enc = '' OR gateway_redis_password_enc IS NULL")
	}
	res := q.UpdateColumn("gateway_redis_password_enc", enc)
	return res.RowsAffected == 1, res.Error
}
func (r *ServerRepository) Delete(id uint) error {
	return r.db.Delete(&models.Server{}, id).Error
}

func (r *ServerRepository) FindByName(name string) (*models.Server, error) {
	var s models.Server
	if err := r.db.Where("name = ?", name).First(&s).Error; err != nil {
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

// CountLicensed counts nodes toward the edition limit, excluding the control plane's own host.
func (r *ServerRepository) CountLicensed() (int64, error) {
	var n int64
	err := r.db.Model(&models.Server{}).Where("is_local = ?", false).Count(&n).Error
	return n, err
}

func (r *ServerRepository) FindByID(id uint) (*models.Server, error) {
	var s models.Server
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// FindBySwarmNodeID matches a self-registering agent to an existing record instead of creating a duplicate.
func (r *ServerRepository) FindBySwarmNodeID(swarmNodeID string) (*models.Server, error) {
	var s models.Server
	if err := r.db.Where("swarm_node_id = ?", swarmNodeID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// Not a full Save: several writers touch this row on agent connect, and a whole-row write can clobber their fields.
func (r *ServerRepository) UpdateSwarmNodeID(id uint, swarmNodeID string) error {
	return r.db.Model(&models.Server{}).
		Where("id = ?", id).
		Update("swarm_node_id", swarmNodeID).Error
}

// Column-scoped like UpdateSwarmNodeID: a full Save would race the cluster refresh loop and agent connect.
func (r *ServerRepository) UpdateEngineVersionBySwarmNodeID(swarmNodeID, version string) error {
	return r.db.Model(&models.Server{}).
		Where("swarm_node_id = ?", swarmNodeID).
		Update("engine_version", version).Error
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
	// Control-plane node defaults to edge-gateway so it can run its own Goma gateway for ingress and TLS.
	s = &models.Server{
		Name: name, DisplayName: name, DockerEndpoint: endpoint, IsLocal: true,
		Role: models.RoleManager, Connectivity: models.ConnectivityEdgeGateway,
		Status: models.ServerStatusUnknown,
	}
	if def, derr := NewClusterRepository(r.db).FindDefault(); derr == nil {
		s.ClusterID = def.ID
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

// Targeted columns, not a full Save: the sweep must not write back a stale copy of a concurrently edited row.
func (r *ServerRepository) SetCapacity(id uint, cores int, memory, storage, storageFree int64, at time.Time) error {
	return r.db.Model(&models.Server{}).Where("id = ?", id).Updates(map[string]any{
		"cpu_cores":            cores,
		"memory_bytes":         memory,
		"storage_bytes":        storage,
		"storage_free_bytes":   storageFree,
		"capacity_measured_at": at,
	}).Error
}

// SetUsage records a node's last measured load.
func (r *ServerRepository) SetUsage(id uint, cpuPercent float64, memUsed int64, at time.Time) error {
	return r.db.Model(&models.Server{}).Where("id = ?", id).Updates(map[string]any{
		"cpu_percent":       cpuPercent,
		"mem_used_bytes":    memUsed,
		"usage_measured_at": at,
	}).Error
}

// Capacity skips never-measured nodes rather than counting them empty; NodesMeasured says how much was seen.
type Capacity struct {
	Nodes            int64   `json:"nodes"`
	NodesMeasured    int64   `json:"nodes_measured"`
	CPUCores         int64   `json:"cpu_cores"`
	MemoryBytes      int64   `json:"memory_bytes"`
	StorageBytes     int64   `json:"storage_bytes"`
	StorageFreeBytes int64   `json:"storage_free_bytes"`
	NodesWithUsage   int64   `json:"nodes_with_usage"`
	CPUPercent       float64 `json:"cpu_percent"` // weighted by core count
	MemUsedBytes     int64   `json:"mem_used_bytes"`
}

// usageMaxAge lets a node that stopped reporting fade out of the figures instead of freezing them.
func (r *ServerRepository) SumCapacity(clusterID uint, usageMaxAge time.Duration) (Capacity, error) {
	var servers []models.Server
	q := r.db.Model(&models.Server{})
	if clusterID != 0 {
		q = q.Where("cluster_id = ?", clusterID)
	}
	if err := q.Find(&servers).Error; err != nil {
		return Capacity{}, err
	}
	return sumCapacity(servers, usageMaxAge), nil
}

// SumCapacityByCluster returns the capacity of every cluster in one pass, for the clusters list.
func (r *ServerRepository) SumCapacityByCluster(usageMaxAge time.Duration) (map[uint]Capacity, error) {
	var servers []models.Server
	if err := r.db.Find(&servers).Error; err != nil {
		return nil, err
	}
	byCluster := map[uint][]models.Server{}
	for i := range servers {
		byCluster[servers[i].ClusterID] = append(byCluster[servers[i].ClusterID], servers[i])
	}
	out := make(map[uint]Capacity, len(byCluster))
	for id, list := range byCluster {
		out[id] = sumCapacity(list, usageMaxAge)
	}
	return out, nil
}

func sumCapacity(servers []models.Server, usageMaxAge time.Duration) Capacity {
	out := Capacity{Nodes: int64(len(servers))}
	cutoff := time.Now().Add(-usageMaxAge)
	var weighted, weight float64
	for i := range servers {
		s := servers[i]
		if s.CapacityMeasuredAt == nil {
			continue
		}
		out.NodesMeasured++
		out.CPUCores += int64(s.CPUCores)
		out.MemoryBytes += s.MemoryBytes
		out.StorageBytes += s.StorageBytes
		out.StorageFreeBytes += s.StorageFreeBytes

		if s.UsageMeasuredAt == nil || s.UsageMeasuredAt.Before(cutoff) {
			continue
		}
		out.NodesWithUsage++
		out.MemUsedBytes += s.MemUsedBytes
		weighted += s.CPUPercent * float64(s.CPUCores)
		weight += float64(s.CPUCores)
	}
	if weight > 0 {
		out.CPUPercent = weighted / weight
	}
	return out
}

// ImageRefs returns each node's pinned edge-gateway image so housekeeping never reclaims it.
func (r *ServerRepository) ImageRefs() ([]string, error) {
	var refs []string
	err := r.db.Model(&models.Server{}).
		Where("gateway_image <> ''").Pluck("gateway_image", &refs).Error
	return refs, err
}
