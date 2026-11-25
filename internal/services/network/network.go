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

// Package network manages workspace-owned Docker networks, created and removed alongside their DB records.
package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/netalloc"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNameRequired  = errors.New("network name is required")
	ErrNameTaken     = errors.New("a network with this name already exists")
	ErrNotFound      = errors.New("network not found")
	ErrInUse         = errors.New("network is attached to one or more applications or databases")
	ErrIsDefault     = errors.New("the default network cannot be deleted")
	ErrInvalidDriver = errors.New("unsupported network driver")
)

// ClusterCap reports whether a cluster runs a swarm, which decides overlay versus bridge for workspace networks.
type ClusterCap interface {
	IsSwarm(clusterID uint) bool
}

type Service struct {
	repo   *repositories.NetworkRepository
	docker docker.Client
	quota  *quota.Service
	alloc  *netalloc.Service
	// nil or cluster mode off keeps plain bridges, no swarm.
	cluster ClusterCap
	// Needed only by the bridge -> overlay migration. See migrate.go.
	clients Resolver
	servers Servers
	dbs     DBInstances
}

func NewService(repo *repositories.NetworkRepository, dockerClient docker.Client) *Service {
	return &Service{repo: repo, docker: dockerClient}
}

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// SetAllocator wires the subnet allocator; nil uses Docker's default pool.
func (s *Service) SetAllocator(a *netalloc.Service) { s.alloc = a }

// SetCluster wires swarm detection; nil means never cluster mode, so networks stay bridges.
func (s *Service) SetCluster(c ClusterCap) { s.cluster = c }

// SetClients wires the per-node Docker client registry used by the migration.
func (s *Service) SetClients(r Resolver) { s.clients = r }

func (s *Service) clusterOn() bool {
	return s.cluster != nil && s.cluster.IsSwarm(models.DefaultClusterID)
}

const (
	DriverBridge  = "bridge"
	DriverOverlay = "overlay"
)

type Input struct {
	// Name is normalized to canonical slug form; DisplayName falls back to Name when blank.
	Name        string
	DisplayName string
	Driver      string
	Internal    bool
}

func (s *Service) Create(ctx context.Context, workspaceID uint, in Input) (*models.Network, error) {
	return s.create(ctx, workspaceID, in, false)
}

// EnsureDefault creates the workspace's default network if it has none.
func (s *Service) EnsureDefault(ctx context.Context, workspaceID uint) (*models.Network, error) {
	nets, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range nets {
		if nets[i].IsDefault {
			return &nets[i], nil
		}
	}
	return s.create(ctx, workspaceID, Input{Name: "default"}, true)
}

func (s *Service) create(ctx context.Context, workspaceID uint, in Input, isDefault bool) (*models.Network, error) {
	name := slug.Make(in.Name, "")
	if name == "" {
		return nil, ErrNameRequired
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(in.Name)
	}
	taken, err := s.repo.ExistsByName(workspaceID, name)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrNameTaken
	}
	// The auto-provisioned default network doesn't consume the workspace quota.
	if !isDefault && s.quota.Enabled() {
		n, _ := s.repo.CountByWorkspace(workspaceID)
		if err := s.quota.CheckCreate(workspaceID, quota.ResourceNetworks, int(n)); err != nil {
			return nil, err
		}
	}
	driver, err := s.driverFor(in.Driver)
	if err != nil {
		return nil, err
	}
	// Overlays always carry the suffix and bridges never do, keeping conversion deterministic. See migrate.go.
	dockerName := fmt.Sprintf("mb-ws%d-%s", workspaceID, randHex(6))
	if driver == DriverOverlay {
		dockerName += overlaySuffix
	}
	dockerCreated := false
	if err := s.provisionDockerNetwork(ctx, dockerName, driver, in.Internal); err != nil {
		// The default network must have a record even with Docker down; deploy reconciles it on each node later.
		if !isDefault {
			return nil, fmt.Errorf("create docker network: %w", err)
		}
		logger.Warn("default network: deferring Docker creation to deploy time",
			"workspace", workspaceID, "docker_name", dockerName, "error", err)
	} else {
		dockerCreated = true
	}
	net := &models.Network{
		WorkspaceID: workspaceID, Name: name, DisplayName: displayName, DockerName: dockerName,
		Driver: driver, Internal: in.Internal, IsDefault: isDefault,
	}
	if err := s.repo.Create(net); err != nil {
		if dockerCreated {
			_ = s.docker.RemoveNetwork(ctx, dockerName) // roll back the docker side
			if s.alloc != nil {
				_ = s.alloc.Release(dockerName) // and free the subnet
			}
		}
		return nil, err
	}
	return net, nil
}

// Miabi picks bridge or overlay from the location; macvlan and ipvlan would put tenant containers on the host's LAN.
func (s *Service) driverFor(requested string) (string, error) {
	switch strings.TrimSpace(requested) {
	case "", DriverBridge, DriverOverlay:
	default:
		return "", fmt.Errorf("%w %q: Miabi chooses the driver from the location, so leave it empty", ErrInvalidDriver, requested)
	}
	if s.clusterOn() {
		return DriverOverlay, nil
	}
	return DriverBridge, nil
}

// An overlay is created on the manager only; swarm materializes it on a worker when a container attaches.
func (s *Service) provisionDockerNetwork(ctx context.Context, dockerName, driver string, internal bool) error {
	spec := networkSpec(dockerName, driver, internal)
	if s.alloc != nil {
		_, _, err := s.alloc.EnsureManaged(ctx, s.docker, spec, 0, allocKind(driver))
		return err
	}
	_, err := s.docker.CreateNetworkSpec(ctx, spec)
	return err
}

// Overlays must be Attachable (plain containers join) and Encrypted (sole protection of east-west traffic).
func networkSpec(dockerName, driver string, internal bool) docker.NetworkSpec {
	spec := docker.NetworkSpec{Name: dockerName, Driver: driver, Internal: internal}
	if driver == DriverOverlay {
		spec.Attachable = true
		spec.Encrypted = true
	}
	return spec
}

func allocKind(driver string) string {
	if driver == DriverOverlay {
		return models.NetAllocKindOverlay
	}
	return models.NetAllocKindWorkspace
}

func (s *Service) Get(workspaceID, id uint) (*models.Network, error) {
	n, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return n, nil
}

func (s *Service) List(workspaceID uint) ([]models.Network, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

func (s *Service) Delete(ctx context.Context, workspaceID, id uint) error {
	n, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	if n.IsDefault {
		return ErrIsDefault
	}
	apps, err := s.repo.CountAppsUsing(n.ID)
	if err != nil {
		return err
	}
	dbs, err := s.repo.CountDatabasesUsing(n.ID)
	if err != nil {
		return err
	}
	if apps+dbs > 0 {
		return ErrInUse
	}
	if err := s.docker.RemoveNetwork(ctx, n.DockerName); err != nil {
		return fmt.Errorf("remove docker network: %w", err)
	}
	s.removeFromNodes(ctx, n.DockerName)
	if s.alloc != nil {
		_ = s.alloc.Release(n.DockerName) // return the subnet to the pool
	}
	return s.repo.Delete(n.ID)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "0000000000"[:n*2]
	}
	return hex.EncodeToString(b)
}

// Detail is a network with the addressing the engine actually gave it; the only place the IPv6 half is visible.
type Detail struct {
	models.Network
	// False when recorded but absent from the engine; addressing fields are then empty rather than stale.
	Exists      bool   `json:"exists"`
	Subnet      string `json:"subnet,omitempty"`
	Gateway     string `json:"gateway,omitempty"`
	IPv6Subnet  string `json:"ipv6_subnet,omitempty"`
	IPv6Gateway string `json:"ipv6_gateway,omitempty"`
	EnableIPv6  bool   `json:"enable_ipv6"`
	Scope       string `json:"scope,omitempty"`
}

// Detail returns Exists false, not an error, for a network the engine lacks: the row still says what the workspace owns.
func (s *Service) Detail(ctx context.Context, workspaceID, id uint) (*Detail, error) {
	n, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	out := &Detail{Network: *n}
	if s.docker == nil {
		return out, nil
	}
	nets, lerr := s.docker.ListNetworks(ctx)
	if lerr != nil {
		return out, nil
	}
	for i := range nets {
		if nets[i].Name != n.DockerName {
			continue
		}
		live := nets[i]
		out.Exists = true
		out.Subnet, out.Gateway = live.Subnet, live.Gateway
		out.IPv6Subnet, out.IPv6Gateway = live.IPv6Subnet, live.IPv6Gateway
		out.EnableIPv6, out.Scope = live.EnableIPv6, live.Scope
		break
	}
	return out, nil
}

const nodeRemoveTimeout = 10 * time.Second

// Deploys recreate a workspace network on every node that runs one of its apps; left there, each copy is an orphan.
func (s *Service) removeFromNodes(ctx context.Context, dockerName string) {
	if s.servers == nil || s.clients == nil {
		return
	}
	servers, err := s.servers.List()
	if err != nil {
		logger.Warn("network delete: could not list nodes; copies on remote nodes stay until removed", "network", dockerName, "error", err)
		return
	}
	for i := range servers {
		srv := &servers[i]
		if srv.IsLocal {
			continue
		}
		dc, err := s.clients.For(srv.ID)
		if err != nil {
			logger.Warn("network delete: node offline; its copy stays until removed from Admin → Networks",
				"network", dockerName, "node", srv.Name)
			continue
		}
		s.removeFromNode(ctx, dc, srv.Name, dockerName)
	}
}

func (s *Service) removeFromNode(ctx context.Context, dc docker.Client, node, dockerName string) {
	ctx, cancel := context.WithTimeout(ctx, nodeRemoveTimeout)
	defer cancel()
	nets, err := dc.ListNetworks(ctx)
	if err != nil {
		logger.Warn("network delete: could not list a node's networks", "network", dockerName, "node", node, "error", err)
		return
	}
	for _, n := range nets {
		if n.Name != dockerName {
			continue
		}
		err := dc.RemoveNetwork(ctx, dockerName)
		switch {
		case err == nil:
			logger.Info("network delete: removed a node's copy", "network", dockerName, "node", node)
		case n.Scope == "swarm":
			// Only a manager can remove an overlay; the cluster's manager drops it for every node.
			logger.Debug("network delete: overlay left to its swarm manager", "network", dockerName, "node", node, "error", err)
		default:
			logger.Warn("network delete: a node's copy is still in use; remove it from Admin → Networks once free",
				"network", dockerName, "node", node, "error", err)
		}
		return
	}
}
