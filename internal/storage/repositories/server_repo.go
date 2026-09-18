// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// FindByName resolves a node by its unique handle — the value
// /api/v1/provider/{name} is built from.
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

func (r *ServerRepository) FindByID(id uint) (*models.Server, error) {
	var s models.Server
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// FindBySwarmNodeID resolves the Miabi node backing a swarm node id. It is how a self-registering
// agent is matched to an existing record instead of creating a duplicate: the agent reports a
// stable identity the control plane can verify against its own `docker node ls`.
func (r *ServerRepository) FindBySwarmNodeID(swarmNodeID string) (*models.Server, error) {
	var s models.Server
	if err := r.db.Where("swarm_node_id = ?", swarmNodeID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateSwarmNodeID writes just that column. Deliberately not a full Save: a node's row is
// written from several places on a single agent connect and by the cluster refresh loop, and a
// read-modify-write of the whole row can silently clobber a field another just set.
func (r *ServerRepository) UpdateSwarmNodeID(id uint, swarmNodeID string) error {
	return r.db.Model(&models.Server{}).
		Where("id = ?", id).
		Update("swarm_node_id", swarmNodeID).Error
}

// UpdateEngineVersionBySwarmNodeID writes just the engine_version column for the
// node with the given swarm node id. Column-scoped for the same reason as
// UpdateSwarmNodeID: the cluster refresh loop and the agent-connect path both
// write this row, and a full Save would race them.
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
	// Control-plane node defaults to manager + edge-gateway so it can run its own
	// Goma gateway for public ingress + TLS. Admin can switch it to cluster (the central gateway).
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

// SetCapacity records what Docker reports a node has. Targeted columns, not a full Save: the sweep
// runs alongside edits from the console and must not write back a stale copy of the rest of the row.
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

// Capacity is the summed capacity of a set of nodes, with the usage of those measured recently.
// Nodes never measured are skipped rather than counted as empty, so the totals describe the fleet
// Miabi can actually see — and NodesMeasured says how much of it that is.
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

// SumCapacity aggregates nodes, optionally scoped to one cluster (0 = every cluster). usageMaxAge
// bounds how old a usage reading may be before it stops counting, so a node that stopped reporting
// fades out of the figures instead of freezing them.
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
