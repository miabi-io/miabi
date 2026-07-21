// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// Resolver resolves the Docker client for a node id (0 = local/manager).
// Satisfied by the nodes.Clients registry.
type Resolver interface {
	For(serverID uint) (docker.Client, error)
}

// Servers lists the nodes whose engines may hold containers on a workspace
// network. Satisfied by repositories.ServerRepository.
type Servers interface {
	List() ([]models.Server, error)
}

// DBInstances repoints database instances pinned to a network by name.
// Satisfied by repositories.DatabaseRepository.
type DBInstances interface {
	RetargetNetwork(oldName, newName string) error
}

// SetMigrationDeps wires the pieces only the bridge -> overlay migration needs.
// All are optional: without them Migrate refuses to run rather than half-doing it.
func (s *Service) SetMigrationDeps(servers Servers, dbs DBInstances) {
	s.servers, s.dbs = servers, dbs
}

// ErrClusterRequired is returned when a driver conversion is attempted while the
// manager is not a swarm manager. Both directions need it: creating an overlay
// requires swarm, and so does detaching containers from one.
var ErrClusterRequired = errors.New("cluster mode must be enabled to convert workspace networks")

// overlaySuffix marks a workspace network as an overlay. The invariant — every
// overlay ends with it, no bridge does — is what makes converting a network in
// either direction a pure function of its current name, so an interrupted run can
// simply be repeated. Enforced on create (network.go) and on convert (below).
const overlaySuffix = "-ov"

// overlayNameFor and bridgeNameFor are inverses.
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
	// OfflineNodes are nodes whose containers could not be moved because their
	// agent was disconnected. Their containers stay on the old network until the
	// next deploy re-attaches them to the (already repointed) workspace network.
	OfflineNodes []string `json:"offline_nodes,omitempty"`
}

// Migrate converts every bridge-backed workspace network into a swarm overlay, so
// containers can reach each other across nodes. It runs when the admin enables
// cluster networking — never on upgrade, never implicitly, and never on a
// single-node install (the overlay driver requires swarm).
func (s *Service) Migrate(ctx context.Context) (MigrationReport, error) {
	return s.convertAll(ctx, DriverBridge, DriverOverlay)
}

// PendingMigration counts the workspace networks still on a node-local bridge.
//
// Non-zero in cluster mode means cross-node east-west does not work yet: those
// workspaces' apps and databases are on per-node islands. That is the normal state
// for an install that was ALREADY in cluster mode when it upgraded to this version
// — Migrate only runs on the enable transition, so nothing has converted it. The
// admin applies it explicitly (cluster.ApplyNetworking); the UI surfaces this count
// to tell them they need to.
func (s *Service) PendingMigration() (int, error) {
	nets, err := s.repo.ListByDriver(DriverBridge)
	return len(nets), err
}

// Rollback converts every overlay-backed workspace network back into a node-local
// bridge. It must run *before* the manager leaves the swarm — once swarm is gone
// the overlays go with it, and every workspace would be left pointing at a network
// that no longer exists. See cluster.Disable.
func (s *Service) Rollback(ctx context.Context) (MigrationReport, error) {
	return s.convertAll(ctx, DriverOverlay, DriverBridge)
}

// convertAll re-drivers every workspace network currently on `from` to `to`.
//
// Containers are NOT restarted. For each network we create the replacement,
// connect every attached container to it (carrying its DNS aliases across
// verbatim), then disconnect and remove the old one. In-flight TCP connections on
// the old network drop; nothing is recreated, and no connection string changes —
// those address a container by its alias, not by its network.
//
// The whole thing is idempotent and resumable: the replacement's name is a pure
// function of the old one, network creation is create-or-reuse, and a network
// whose record has already flipped driver is no longer a candidate. A single
// failing workspace is reported and skipped; it never strands the rest.
func (s *Service) convertAll(ctx context.Context, from, to string) (MigrationReport, error) {
	var rep MigrationReport
	// Both directions need a live swarm: one to create an overlay, the other to
	// still be able to detach containers from one.
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
	// Resolve every node's engine once. A node whose agent is offline cannot have
	// its containers moved — record it and carry on rather than blocking every
	// other workspace on one unreachable box.
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

// convertOne re-drivers a single workspace network.
func (s *Service) convertOne(ctx context.Context, n *models.Network, newName, toDriver string, forEachEngine func(func(string, docker.Client))) error {
	// 1. Create the replacement on the manager. An overlay is swarm-scoped — Docker
	//    materializes it on a worker only when a container there attaches — so it is
	//    never created per node. Create-or-reuse, so a resumed run picks up where it
	//    left off. (A bridge is created per node as containers move; see step 2.)
	if err := s.provisionDockerNetwork(ctx, newName, toDriver, n.Internal); err != nil {
		return fmt.Errorf("create %s network %s: %w", toDriver, newName, err)
	}

	// 2. A bridge is node-local, so it must exist on each node before a container
	//    there can join it — recreated from the same pool subnet, exactly as
	//    syncNetworks does at deploy time. (An overlay needs none of this: swarm
	//    materializes it on the node at attach time.)
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

	// 3. Move every attached container across, on every reachable node, carrying its
	//    DNS aliases so names keep resolving. Connect before disconnect: the
	//    container is never left without a network.
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

	// 4. Repoint the records BEFORE removing the old network. If we die here, the
	//    old network lingers as a harmless empty one and a re-run is a no-op — far
	//    better than records pointing at a network that no longer exists.
	if err := s.dbs.RetargetNetwork(n.DockerName, newName); err != nil {
		return fmt.Errorf("repoint database instances: %w", err)
	}
	oldName := n.DockerName
	n.DockerName, n.Driver = newName, toDriver
	if err := s.repo.Update(n); err != nil {
		return fmt.Errorf("update network record: %w", err)
	}

	// 5. Tear the old network down everywhere and return its subnet to the pool.
	//    Best-effort: a leftover empty network is cosmetic, not a failure.
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

// moveContainers connects every container attached to `from` on this engine to
// `to`, preserving its DNS aliases, then detaches it from `from`.
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
		// Aliases are the only stable way to address the container (its IP is
		// ephemeral and its name carries a deployment id), so they must survive the
		// move verbatim — recomputing them would silently break routing.
		if err := dc.NetworkConnect(ctx, to, c.ID, aliases); err != nil {
			return fmt.Errorf("connect %s to %s: %w", shortID(c.ID), to, err)
		}
		if err := dc.NetworkDisconnect(ctx, from, c.ID, true); err != nil {
			// The container is already on the overlay, so it is reachable. Leaving it
			// dual-homed is survivable; failing the whole workspace here is not.
			logger.Warn("network migration: could not detach from the old bridge",
				"container", shortID(c.ID), "network", from, "error", err)
		}
	}
	return nil
}

// aliasesOn returns the container's DNS aliases on the named network, and whether
// it is attached to it at all.
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
