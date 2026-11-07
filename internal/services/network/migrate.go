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

package network

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type Resolver interface {
	For(serverID uint) (docker.Client, error)
}

type Servers interface {
	List() ([]models.Server, error)
}

type DBInstances interface {
	RetargetNetwork(oldName, newName string) error
}

// SetMigrationDeps is optional; without it Migrate refuses to run rather than half-doing it.
func (s *Service) SetMigrationDeps(servers Servers, dbs DBInstances) {
	s.servers, s.dbs = servers, dbs
}

// ErrClusterRequired: both conversion directions need swarm, to create an overlay or to detach containers from one.
var ErrClusterRequired = errors.New("cluster mode must be enabled to convert workspace networks")

// Every overlay ends with this suffix and no bridge does, so an interrupted conversion can simply be repeated.
const overlaySuffix = "-ov"

func overlayNameFor(bridgeName string) string {
	return strings.TrimSuffix(bridgeName, overlaySuffix) + overlaySuffix
}

func bridgeNameFor(overlayName string) string {
	return strings.TrimSuffix(overlayName, overlaySuffix)
}

// peerName is the Docker name the network takes once converted to toDriver.
func peerName(dockerName, toDriver string) string {
	if toDriver == DriverOverlay {
		return overlayNameFor(dockerName)
	}
	return bridgeNameFor(dockerName)
}

// MigrationReport summarizes one conversion run.
type MigrationReport struct {
	Migrated []string `json:"migrated"`         // docker names of the networks after conversion
	Failed   []string `json:"failed,omitempty"` // networks left on their old driver (see logs)
	// Containers on offline nodes stay on the old network until the next deploy re-attaches them.
	OfflineNodes []string `json:"offline_nodes,omitempty"`
}

// Migrate converts bridge workspace networks to overlays; runs only when the admin enables cluster networking.
func (s *Service) Migrate(ctx context.Context) (MigrationReport, error) {
	return s.convertAll(ctx, DriverBridge, DriverOverlay)
}

// PendingMigration is non-zero for installs clustered before upgrade, since Migrate runs only on the enable transition.
func (s *Service) PendingMigration() (int, error) {
	nets, err := s.repo.ListByDriver(DriverBridge)
	return len(nets), err
}

// Rollback must run before the manager leaves the swarm, or the overlays vanish with it. See cluster.Disable.
func (s *Service) Rollback(ctx context.Context) (MigrationReport, error) {
	return s.convertAll(ctx, DriverOverlay, DriverBridge)
}

// Moves containers without restarting them; idempotent and resumable, a failing workspace is reported and skipped.
func (s *Service) convertAll(ctx context.Context, from, to string) (MigrationReport, error) {
	var rep MigrationReport
	// Both directions need a live swarm: one to create an overlay, the other to detach containers from one.
	if !s.clusterOn() {
		return rep, ErrClusterRequired
	}
	if s.clients == nil || s.servers == nil || s.dbs == nil {
		return rep, errors.New("network migration is not wired (clients/servers/databases)")
	}
	nets, err := s.repo.ListByDriver(from)
	if err != nil {
		return rep, err
	}
	if len(nets) == 0 {
		return rep, nil
	}
	servers, err := s.servers.List()
	if err != nil {
		return rep, err
	}
	// An offline node's containers cannot be moved; record it and carry on instead of blocking other workspaces.
	type engine struct {
		name string
		dc   docker.Client
	}
	var engines []engine
	for i := range servers {
		srv := servers[i]
		dc, derr := s.clients.For(srv.ID)
		if derr != nil {
			rep.OfflineNodes = append(rep.OfflineNodes, srv.Name)
			logger.Warn("network migration: node offline, its containers stay on the bridge until redeploy",
				"node", srv.Name, "error", derr)
			continue
		}
		engines = append(engines, engine{name: srv.Name, dc: dc})
	}

	for i := range nets {
		n := nets[i]
		newName := peerName(n.DockerName, to)
		if err := s.convertOne(ctx, &n, newName, to, func(fn func(string, docker.Client)) {
			for _, e := range engines {
				fn(e.name, e.dc)
			}
		}); err != nil {
			logger.Error("network conversion failed; workspace stays on its current network",
				"workspace", n.WorkspaceID, "network", n.DockerName, "from", from, "to", to, "error", err)
			rep.Failed = append(rep.Failed, n.DockerName)
			continue
		}
		rep.Migrated = append(rep.Migrated, newName)
	}
	logger.Info("workspace network conversion complete", "from", from, "to", to,
		"migrated", len(rep.Migrated), "failed", len(rep.Failed), "offline_nodes", len(rep.OfflineNodes))
	return rep, nil
}

func (s *Service) convertOne(ctx context.Context, n *models.Network, newName, toDriver string, forEachEngine func(func(string, docker.Client))) error {
	// An overlay is swarm-scoped (materialized at attach time), so it is never created per node.
	if err := s.provisionDockerNetwork(ctx, newName, toDriver, n.Internal); err != nil {
		return fmt.Errorf("create %s network %s: %w", toDriver, newName, err)
	}

	// A bridge is node-local, so it must exist on each node first, from the same pool subnet as syncNetworks uses.
	var moveErr error
	if toDriver == DriverBridge && s.alloc != nil {
		forEachEngine(func(nodeName string, dc docker.Client) {
			if moveErr != nil {
				return
			}
			spec := networkSpec(newName, DriverBridge, n.Internal)
			if _, _, err := s.alloc.EnsureManaged(ctx, dc, spec, 0, models.NetAllocKindWorkspace); err != nil {
				moveErr = fmt.Errorf("node %s: recreate bridge %s: %w", nodeName, newName, err)
			}
		})
		if moveErr != nil {
			return moveErr
		}
	}

	// Connect before disconnect so the container is never left without a network.
	forEachEngine(func(nodeName string, dc docker.Client) {
		if moveErr != nil {
			return
		}
		if err := s.moveContainers(ctx, dc, n.DockerName, newName); err != nil {
			moveErr = fmt.Errorf("node %s: %w", nodeName, err)
		}
	})
	if moveErr != nil {
		return moveErr
	}

	// Repoint records before removing the old network: a crash then leaves only a harmless empty network.
	if err := s.dbs.RetargetNetwork(n.DockerName, newName); err != nil {
		return fmt.Errorf("repoint database instances: %w", err)
	}
	oldName := n.DockerName
	n.DockerName, n.Driver = newName, toDriver
	if err := s.repo.Update(n); err != nil {
		return fmt.Errorf("update network record: %w", err)
	}

	// Best-effort: a leftover empty network is cosmetic, not a failure.
	forEachEngine(func(nodeName string, dc docker.Client) {
		if err := dc.RemoveNetwork(ctx, oldName); err != nil {
			logger.Warn("network conversion: could not remove the old network",
				"node", nodeName, "network", oldName, "error", err)
		}
	})
	if s.alloc != nil {
		_ = s.alloc.Release(oldName)
	}
	logger.Info("workspace network converted",
		"workspace", n.WorkspaceID, "from", oldName, "to", newName, "driver", toDriver)
	return nil
}

func (s *Service) moveContainers(ctx context.Context, dc docker.Client, from, to string) error {
	containers, err := dc.ListContainers(ctx, true) // include stopped: they must move too
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	for _, c := range containers {
		full, err := dc.InspectContainer(ctx, c.ID)
		if err != nil {
			continue // vanished mid-migration; nothing to move
		}
		aliases, attached := aliasesOn(full, from)
		if !attached {
			continue
		}
		// Aliases are the only stable address (IPs are ephemeral), so they must survive the move verbatim.
		if err := dc.NetworkConnect(ctx, to, c.ID, aliases); err != nil {
			return fmt.Errorf("connect %s to %s: %w", shortID(c.ID), to, err)
		}
		if err := dc.NetworkDisconnect(ctx, from, c.ID, true); err != nil {
			// The container is already on the new network; staying dual-homed is survivable, failing the workspace is not.
			logger.Warn("network migration: could not detach from the old bridge",
				"container", shortID(c.ID), "network", from, "error", err)
		}
	}
	return nil
}

func aliasesOn(c docker.Container, network string) ([]string, bool) {
	for _, n := range c.Networks {
		if n.Name == network {
			return n.Aliases, true
		}
	}
	return nil, false
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
