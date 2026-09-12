// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package placement decides where a new resource lands: a location (cluster) the workspace may use, then a
// node inside it. It runs once, at create; nothing is rescheduled afterwards.
package placement

import (
	"errors"
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
}

func NewService(clusters *repositories.ClusterRepository, servers *repositories.ServerRepository, online Online) *Service {
	return &Service{clusters: clusters, servers: servers, online: online}
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
	serverID, err := s.pickNode(c, req.Service)
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

func allowed(c *models.Cluster, admin bool) bool {
	return admin || c.Visibility != models.ClusterVisibilityRestricted
}

// resolveCluster picks the named location, else the workspace's default, else the first location the
// workspace may use (the default cluster comes first).
func (s *Service) resolveCluster(workspaceID uint, location string, admin bool) (*models.Cluster, error) {
	if name := strings.TrimSpace(location); name != "" {
		c, err := s.clusters.FindByName(name)
		if err != nil {
			return nil, ErrLocationNotFound
		}
		if !allowed(c, admin) {
			return nil, ErrLocationNotAllowed
		}
		if c.Cordoned {
			return nil, ErrLocationCordoned
		}
		return c, nil
	}
	if def, err := s.clusters.WorkspaceDefault(workspaceID); err == nil && allowed(def, admin) && !def.Cordoned {
		return def, nil
	}
	list, err := s.clusters.List()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if allowed(&list[i], admin) && !list[i].Cordoned {
			return &list[i], nil
		}
	}
	return nil, ErrNoLocation
}

// pickNode chooses the node inside a cluster: its only node when standalone, the manager for a service,
// and otherwise the online, uncordoned node with the least container memory already placed on it.
func (s *Service) pickNode(c *models.Cluster, service bool) (uint, error) {
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
		load := loads[srv.ID]
		if best == 0 || load.Less(bestLoad) {
			best, bestLoad = srv.ID, load
		}
	}
	if best == 0 {
		return 0, ErrNoSchedulableNode
	}
	return best, nil
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
	out := make([]Location, 0, len(list))
	for i := range list {
		c := &list[i]
		if !allowed(c, admin) || c.Cordoned {
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
	if !allowed(c, admin) {
		return ErrLocationNotAllowed
	}
	return s.clusters.SetWorkspaceDefault(workspaceID, &c.ID)
}
