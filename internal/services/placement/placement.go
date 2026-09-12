// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package placement decides where a new resource lands: a location (cluster) the workspace may use, then a
// node inside it. It runs once, at create; nothing is rescheduled afterwards.
package placement

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrLocationNotFound   = errors.New("no such location")
	ErrLocationNotAllowed = errors.New("this workspace cannot use that location")
	ErrLocationCordoned   = errors.New("that location accepts no new resources")
	ErrNoLocation         = errors.New("no location is available to this workspace")
	ErrNoSchedulableNode  = errors.New("no node in that location can take new resources right now")
	ErrNodePinAdminOnly   = errors.New("only platform admins can choose a node; choose a location instead")
	ErrLocationMismatch   = errors.New("the chosen node is not in the chosen location")
)

// Online reports whether a remote node's agent is connected. Satisfied by nodes.Clients.Connected.
type Online func(serverID uint) bool

// Service places new resources.
type Service struct {
	clusters *repositories.ClusterRepository
	servers  *repositories.ServerRepository
	online   Online
	policy   Policy
}

func NewService(clusters *repositories.ClusterRepository, servers *repositories.ServerRepository, online Online) *Service {
	return &Service{clusters: clusters, servers: servers, online: online}
}

// Policy resolves the locations and node pool a workspace's plan binds it to; false means no policy applies.
// Satisfied by the quota service.
type Policy interface {
	EffectivePlacement(workspaceID uint) (models.PlanPlacement, bool)
}

// SetPolicy wires plan placement (nil-safe; nil binds no workspace to locations or pools).
func (s *Service) SetPolicy(p Policy) { s.policy = p }

func (s *Service) planPlacement(workspaceID uint) (models.PlanPlacement, bool) {
	if s.policy == nil {
		return models.PlanPlacement{}, false
	}
	return s.policy.EffectivePlacement(workspaceID)
}

func permits(p models.PlanPlacement, enforced bool, clusterID uint) bool {
	return !enforced || len(p.Locations) == 0 || slices.Contains(p.Locations, clusterID)
}

// Request describes a resource to place.
type Request struct {
	WorkspaceID uint
	// Location is a location (cluster) name; empty uses the workspace's default location.
	Location string
	// ServerID pins a node, which only platform admins may do.
	ServerID uint
	// Colocate is a node the platform itself requires, such as the node of a database being reused.
	Colocate uint
	Admin    bool
	// Service marks a replicated service app: Swarm schedules it, so its node is only the manager.
	Service bool
}

// Result is where the resource lands.
type Result struct {
	ClusterID uint `json:"cluster_id"`
	ServerID  uint `json:"server_id"`
}

// Place resolves the cluster and node a new resource lands on.
func (s *Service) Place(req Request) (Result, error) {
	if req.ServerID != 0 && !req.Admin {
		return Result{}, ErrNodePinAdminOnly
	}
	if req.ServerID != 0 {
		return s.onNode(req.ServerID, req.Location)
	}
	if req.Colocate != 0 {
		return s.onNode(req.Colocate, req.Location)
	}
	c, err := s.resolveCluster(req.WorkspaceID, req.Location, req.Admin)
	if err != nil {
		return Result{}, err
	}
	policy, enforced := s.planPlacement(req.WorkspaceID)
	serverID, err := s.pickNode(c, req.Service, policy.Pool, enforced)
	if err != nil {
		return Result{}, err
	}
	return Result{ClusterID: c.ID, ServerID: serverID}, nil
}

func (s *Service) onNode(serverID uint, location string) (Result, error) {
	srv, err := s.servers.FindByID(serverID)
	if err != nil {
		return Result{}, node.ErrNodeNotFound
	}
	if name := strings.TrimSpace(location); name != "" {
		c, err := s.clusters.FindByName(name)
		if err != nil {
			return Result{}, ErrLocationNotFound
		}
		if c.ID != srv.ClusterID {
			return Result{}, ErrLocationMismatch
		}
	}
	return Result{ClusterID: srv.ClusterID, ServerID: serverID}, nil
}

// ResolveLocation is the cluster a create naming location lands in: that location, else the workspace
// default, else the first location the workspace may use.
func (s *Service) ResolveLocation(workspaceID uint, location string, admin bool) (*models.Cluster, error) {
	return s.resolveCluster(workspaceID, location, admin)
}

func allowed(c *models.Cluster, admin bool) bool {
	return admin || c.Visibility != models.ClusterVisibilityRestricted
}

// resolveCluster picks the named location, else the workspace's default, else the first location the
// workspace may use (the default cluster comes first).
func (s *Service) resolveCluster(workspaceID uint, location string, admin bool) (*models.Cluster, error) {
	policy, enforced := s.planPlacement(workspaceID)
	usable := func(c *models.Cluster) bool {
		return allowed(c, admin) && !c.Cordoned && permits(policy, enforced, c.ID)
	}
	if name := strings.TrimSpace(location); name != "" {
		c, err := s.clusters.FindByName(name)
		if err != nil {
			return nil, ErrLocationNotFound
		}
		if !allowed(c, admin) || !permits(policy, enforced, c.ID) {
			return nil, ErrLocationNotAllowed
		}
		if c.Cordoned {
			return nil, ErrLocationCordoned
		}
		return c, nil
	}
	if def, err := s.clusters.WorkspaceDefault(workspaceID); err == nil && usable(def) {
		return def, nil
	}
	if enforced {
		for _, id := range policy.Locations {
			if c, err := s.clusters.FindByID(id); err == nil && usable(c) {
				return c, nil
			}
		}
	}
	list, err := s.clusters.List()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if usable(&list[i]) {
			return &list[i], nil
		}
	}
	return nil, ErrNoLocation
}

// pickNode chooses the node inside a cluster: its only node when standalone, the manager for a service,
// and otherwise the online, uncordoned node with the least container memory already placed on it. With a
// pooled plan only nodes in the plan's pool count, and a cluster with none of them refuses the create.
func (s *Service) pickNode(c *models.Cluster, service bool, pool string, pooled bool) (uint, error) {
	if pooled && (c.Mode != models.ClusterModeSwarm || service) {
		servers, err := s.servers.List()
		if err != nil {
			return 0, err
		}
		if !slices.ContainsFunc(servers, func(srv models.Server) bool {
			return srv.ClusterID == c.ID && !srv.Cordoned && models.PoolOf(&srv) == pool
		}) {
			return 0, poolUnavailable(pool)
		}
	}
	if c.Mode != models.ClusterModeSwarm || service {
		if c.IsDefault {
			local, err := s.servers.FindLocal()
			if err != nil {
				return 0, ErrNoSchedulableNode
			}
			return local.ID, nil
		}
		return c.ManagerServerID, nil
	}
	servers, err := s.servers.List()
	if err != nil {
		return 0, err
	}
	loads, err := s.clusters.ServerLoads(c.ID)
	if err != nil {
		return 0, err
	}
	var best uint
	var bestLoad repositories.ServerLoad
	for i := range servers {
		srv := &servers[i]
		if srv.ClusterID != c.ID || srv.Cordoned || (!srv.IsLocal && (s.online == nil || !s.online(srv.ID))) {
			continue
		}
		if pooled && models.PoolOf(srv) != pool {
			continue
		}
		load := loads[srv.ID]
		if best == 0 || load.Less(bestLoad) {
			best, bestLoad = srv.ID, load
		}
	}
	if best == 0 && pooled {
		return 0, poolUnavailable(pool)
	}
	if best == 0 {
		return 0, ErrNoSchedulableNode
	}
	return best, nil
}

func poolUnavailable(pool string) error {
	if pool == "" {
		return fmt.Errorf("%w: every node there is in a pool this workspace's plan does not use", ErrNoSchedulableNode)
	}
	return fmt.Errorf("%w: none is in the %q pool this workspace's plan requires", ErrNoSchedulableNode, pool)
}

// PoolConstraints are the Swarm constraints that keep a workspace's services in its plan's pool: that pool's
// label, or for a plan without a pool, every pool present in the cluster excluded. Nil when no policy applies.
func (s *Service) PoolConstraints(workspaceID, clusterID uint) []string {
	policy, enforced := s.planPlacement(workspaceID)
	if !enforced {
		return nil
	}
	if policy.Pool != "" {
		return []string{fmt.Sprintf("node.labels.%s==%s", models.PoolLabel, policy.Pool)}
	}
	servers, err := s.servers.List()
	if err != nil {
		return nil
	}
	clusterID = s.resolveID(clusterID)
	seen := map[string]bool{}
	var out []string
	for i := range servers {
		if p := models.PoolOf(&servers[i]); p != "" && servers[i].ClusterID == clusterID && !seen[p] {
			seen[p] = true
			out = append(out, fmt.Sprintf("node.labels.%s!=%s", models.PoolLabel, p))
		}
	}
	sort.Strings(out)
	return out
}

// Location is a cluster as a workspace sees it: no nodes, addresses or capacity.
type Location struct {
	ID           uint   `json:"id"`
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	LocationCode string `json:"location_code,omitempty"`
	// Default marks the location a create lands in when it names none.
	Default bool `json:"default"`
}

// Locations lists the locations a workspace may place new resources in.
func (s *Service) Locations(workspaceID uint, admin bool) ([]Location, error) {
	list, err := s.clusters.List()
	if err != nil {
		return nil, err
	}
	fallback, _ := s.resolveCluster(workspaceID, "", admin)
	policy, enforced := s.planPlacement(workspaceID)
	out := make([]Location, 0, len(list))
	for i := range list {
		c := &list[i]
		if !allowed(c, admin) || c.Cordoned || !permits(policy, enforced, c.ID) {
			continue
		}
		out = append(out, Location{
			ID:           c.ID,
			Name:         c.Name,
			DisplayName:  c.Label(),
			LocationCode: c.LocationCode,
			Default:      fallback != nil && fallback.ID == c.ID,
		})
	}
	return out, nil
}

// SetDefaultLocation sets the location a workspace's new resources land in when a create names none.
// An empty name clears it.
func (s *Service) SetDefaultLocation(workspaceID uint, location string, admin bool) error {
	name := strings.TrimSpace(location)
	if name == "" {
		return s.clusters.SetWorkspaceDefault(workspaceID, nil)
	}
	c, err := s.clusters.FindByName(name)
	if err != nil {
		return ErrLocationNotFound
	}
	if policy, enforced := s.planPlacement(workspaceID); !allowed(c, admin) || !permits(policy, enforced, c.ID) {
		return ErrLocationNotAllowed
	}
	return s.clusters.SetWorkspaceDefault(workspaceID, &c.ID)
}
