// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cluster auto-detects whether the manager's Docker engine is in swarm
// mode and, when it is, drives Docker Swarm as the internal implementation of
// Miabi's optional cluster mode. Single-node on plain Docker stays first-class:
// when the engine is not a reachable swarm manager, CapCluster is false and
// every cluster operation is a guarded no-op.
//
// Swarm is a deliberate seam behind docker.Client so a future
// containerd/Kubernetes backend can replace it without touching callers.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

var (
	// ErrNotEnabled is returned when a cluster operation needs swarm mode but the
	// manager is not a reachable swarm manager.
	ErrNotEnabled = errors.New("cluster mode is not enabled")
	// ErrAdvertiseAddrRequired is returned when enabling cluster mode without an
	// advertise address (the address peers reach this manager on).
	ErrAdvertiseAddrRequired = errors.New("an advertise address is required to enable cluster mode")
	// ErrManagerNode is returned when an operation that targets a worker node is
	// pointed at the manager.
	ErrManagerNode = errors.New("the manager node cannot be used for this operation")
	// ErrManagerAddrUnknown is returned when the manager's swarm address has not
	// been detected yet (refresh first).
	ErrManagerAddrUnknown = errors.New("manager swarm address is unknown; refresh cluster state")
)

// swarmStateActive is the LocalNodeState value for an engine that has joined a
// swarm.
const swarmStateActive = "active"

// NodeDocker resolves Docker clients per node (0/local = the manager engine).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	Local() docker.Client
	LocalID() uint
}

// Nodes is the slice of the node service the cluster service depends on.
type Nodes interface {
	List(ctx context.Context) ([]models.Server, error)
	Get(id uint) (*models.Server, error)
	SetSwarmNodeID(id uint, swarmNodeID string) error
}

// Service tracks the manager's swarm state and exposes cluster operations.
type Service struct {
	clients NodeDocker
	nodes   Nodes

	mu          sync.RWMutex
	info        docker.SwarmInfo            // manager's swarm state (last refresh)
	swarm       map[string]docker.SwarmNode // swarm node id -> node (last refresh)
	refreshedAt time.Time

	// ingressReconciler re-asserts the central gateway's attachment to the shared
	// cluster ingress overlay, run on each refresh so a gateway recreate (compose
	// up -d) can't leave clustered apps publicly dark for longer than a refresh
	// interval. Optional (nil = no-op); wired after construction.
	ingressReconciler func(context.Context) error

	// networkMigrator converts every workspace's node-local bridge into a swarm
	// overlay, so containers reach each other across nodes. Run once, when the
	// admin turns cluster mode on — never on upgrade and never implicitly, because
	// it briefly drops in-flight connections inside each workspace. Optional
	// (nil = no-op); wired after construction. See services/network.Migrate.
	networkMigrator func(context.Context) error
	// networkRollback is its inverse, run on Disable *before* leaving the swarm:
	// the overlays die with the swarm, so every workspace must be back on a bridge
	// first or it would be left pointing at a network that no longer exists.
	networkRollback func(context.Context) error
}

// SetNetworkMigrator wires the workspace-network driver conversion: `migrate`
// (bridge -> overlay) runs on Enable, `rollback` (overlay -> bridge) on Disable.
// Nil-safe; nil leaves networks on whatever driver they already have.
func (s *Service) SetNetworkMigrator(migrate, rollback func(context.Context) error) {
	s.networkMigrator, s.networkRollback = migrate, rollback
}

// migrateNetworks converts workspace bridges to overlays now that swarm is up.
// Best-effort at the call site: a failure is logged and reported per workspace by
// the migration itself, and leaves those workspaces on their bridge (i.e. exactly
// as they are today) rather than failing the whole enable.
func (s *Service) migrateNetworks(ctx context.Context) {
	if s.networkMigrator == nil {
		return
	}
	if err := s.networkMigrator(ctx); err != nil {
		logger.Warn("cluster enabled, but migrating workspace networks to overlays failed", "error", err)
	}
}

// NewService builds the cluster service. Call Refresh once at boot to populate
// the initial swarm state.
func NewService(clients NodeDocker, nodes Nodes) *Service {
	return &Service{clients: clients, nodes: nodes, swarm: map[string]docker.SwarmNode{}}
}

// SetIngressReconciler wires the callback that re-asserts the central gateway's
// attachment to the shared cluster ingress overlay. Called on every Refresh, so a
// gateway recreate can't strand ingress to clustered apps for long. Nil-safe.
func (s *Service) SetIngressReconciler(fn func(context.Context) error) {
	s.mu.Lock()
	s.ingressReconciler = fn
	s.mu.Unlock()
}

// CapCluster reports whether the manager is a reachable swarm manager, the gate
// every cluster feature is conditioned on. False on plain Docker.
func (s *Service) CapCluster() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.capLocked()
}

func (s *Service) capLocked() bool {
	return s.info.LocalNodeState == swarmStateActive && s.info.ControlAvailable
}

// Status is the cluster status surfaced to the API / Nodes page.
type Status struct {
	// Enabled mirrors CapCluster: the manager is a reachable swarm manager.
	Enabled bool `json:"enabled"`
	// LocalNodeState is the manager engine's swarm state (inactive on plain
	// Docker).
	LocalNodeState string `json:"local_node_state"`
	// ManagerAddr is the address the manager advertises to swarm peers (the
	// remote address workers join against).
	ManagerAddr string `json:"manager_addr,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Managers    int    `json:"managers"`
	Nodes       int    `json:"nodes"`
	// IngressNetwork is the shared, attachable overlay the reverse proxy joins to
	// reach clustered apps' service VIPs (north-south ingress). Non-empty only when
	// cluster mode is on. Miabi attaches its own managed gateway automatically; an
	// admin running their own reverse proxy can attach it by hand:
	//   docker network connect <ingress_network> <their-proxy-container>
	IngressNetwork string `json:"ingress_network,omitempty"`
	Error          string `json:"error,omitempty"`
}

// Status returns the last-refreshed cluster status.
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{
		Enabled:        s.capLocked(),
		LocalNodeState: s.info.LocalNodeState,
		ManagerAddr:    s.info.NodeAddr,
		NodeID:         s.info.NodeID,
		Managers:       s.info.Managers,
		Nodes:          s.info.Nodes,
		Error:          s.info.Error,
	}
	// Advertise the ingress overlay only while cluster mode is on, so the UI can
	// tell an admin how to attach a self-managed reverse proxy to it.
	if st.Enabled {
		st.IngressNetwork = node.IngressOverlay
	}
	return st
}

// Refresh re-reads the manager's swarm state and (when it is a manager) its
// node list. Cheap and safe on plain Docker. Called at boot and on an interval.
func (s *Service) Refresh(ctx context.Context) {
	local := s.clients.Local()
	info, err := local.Swarm(ctx)
	if err != nil {
		logger.Warn("failed to read swarm state", "error", err)
		// Treat an unreadable engine as inactive rather than holding stale state.
		info = docker.SwarmInfo{}
	}
	nodesByID := map[string]docker.SwarmNode{}
	if info.ControlAvailable {
		if list, lerr := local.SwarmNodes(ctx); lerr == nil {
			for _, n := range list {
				nodesByID[n.ID] = n
			}
		} else {
			logger.Warn("failed to list swarm nodes", "error", lerr)
		}
	}
	s.mu.Lock()
	s.info = info
	s.swarm = nodesByID
	s.refreshedAt = time.Now()
	ingress := s.ingressReconciler
	s.mu.Unlock()

	// Persist the manager's own swarm node id so the Nodes page can correlate it.
	if info.NodeID != "" {
		if id := s.clients.LocalID(); id != 0 {
			if serr := s.nodes.SetSwarmNodeID(id, info.NodeID); serr != nil {
				logger.Warn("failed to persist manager swarm node id", "error", serr)
			}
		}
	}

	// Re-assert the central gateway's ingress-overlay attachment while cluster mode
	// is on (a gateway recreate drops the runtime attachment; this heals it). Cheap
	// and self-gating: the reconciler no-ops when the gateway isn't found.
	if ingress != nil && info.ControlAvailable {
		if err := ingress(ctx); err != nil {
			logger.Warn("failed to reconcile cluster ingress gateway", "error", err)
		}
	}
}

// RefreshLoop refreshes swarm state on the given interval until ctx is done.
func (s *Service) RefreshLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Refresh(ctx)
		}
	}
}

// Enrich annotates each server's transient swarm fields from the last refresh,
// so the Nodes page shows swarm role/availability alongside standalone nodes.
// A no-op (fields stay blank) when cluster mode is off.
func (s *Service) Enrich(servers []models.Server) {
	s.mu.RLock()
	enabled := s.capLocked()
	swarmNodes := s.swarm
	managerNodeID := s.info.NodeID
	s.mu.RUnlock()
	if !enabled {
		return
	}
	for i := range servers {
		srv := &servers[i]
		id := srv.SwarmNodeID
		if id == "" && srv.IsLocal {
			id = managerNodeID
		}
		n, ok := swarmNodes[id]
		if !ok || id == "" {
			n, ok = matchByHostname(swarmNodes, srv)
		}
		if !ok {
			srv.SwarmRole = "standalone"
			continue
		}
		srv.InSwarm = true
		srv.SwarmRole = roleOf(n)
		srv.SwarmAvailability = n.Availability
		srv.SwarmState = n.State
	}
}

// roleOf maps a swarm node to the role shown in the UI: the leading manager is
// "leader", other managers "manager", and workers "worker".
func roleOf(n docker.SwarmNode) string {
	if n.Leader {
		return "leader"
	}
	if n.Role != "" {
		return n.Role
	}
	return "worker"
}

// matchByHostname is the fallback correlation when a node's swarm id is not yet
// stored: match the swarm node by hostname against the server's hostname/name.
func matchByHostname(swarmNodes map[string]docker.SwarmNode, srv *models.Server) (docker.SwarmNode, bool) {
	candidates := []string{srv.PublicHostname, srv.Name}
	for _, n := range swarmNodes {
		for _, c := range candidates {
			if c != "" && strings.EqualFold(n.Hostname, c) {
				return n, true
			}
		}
	}
	return docker.SwarmNode{}, false
}

// Enable puts the manager engine into swarm mode as the first manager, or
// adopts a pre-existing swarm if Docker is already in one. advertiseAddr is the
// address peers reach this manager on (its private/WG address); ignored when
// adopting an existing swarm.
func (s *Service) Enable(ctx context.Context, advertiseAddr string) (Status, error) {
	local := s.clients.Local()
	info, err := local.Swarm(ctx)
	if err != nil {
		return Status{}, err
	}
	if info.LocalNodeState == swarmStateActive {
		// Already in a swarm (set up by Miabi previously or by the admin). Adopt it.
		s.Refresh(ctx)
		if !s.CapCluster() {
			return Status{}, errors.New("docker is in swarm mode but this engine is not a reachable manager")
		}
		s.ensureIngressOverlay(ctx)
		s.migrateNetworks(ctx)
		logger.Info("adopted existing docker swarm", "node_id", info.NodeID)
		return s.Status(), nil
	}
	addr := strings.TrimSpace(advertiseAddr)
	if addr == "" {
		return Status{}, ErrAdvertiseAddrRequired
	}
	nodeID, err := local.SwarmInit(ctx, docker.SwarmInitRequest{AdvertiseAddr: addr})
	if err != nil {
		return Status{}, err
	}
	logger.Info("initialized docker swarm", "node_id", nodeID, "advertise", addr)
	s.Refresh(ctx)
	s.ensureIngressOverlay(ctx)
	// Refresh first: the migration refuses to run until CapCluster() is true.
	s.migrateNetworks(ctx)
	return s.Status(), nil
}

// ensureIngressOverlay pre-creates the shared, attachable cluster ingress overlay
// as soon as cluster mode comes up, so the central gateway — or a reverse proxy
// the admin manages themselves and attaches by hand (docker network connect
// miabi-ingress <proxy>) — has a network to join before the first clustered app
// is deployed. Best-effort: a failure is logged, and the deploy/refresh paths
// retry the create anyway.
func (s *Service) ensureIngressOverlay(ctx context.Context) {
	if _, err := s.clients.Local().CreateOverlayNetwork(ctx, node.IngressOverlay); err != nil {
		logger.Warn("failed to ensure cluster ingress overlay", "network", node.IngressOverlay, "error", err)
	}
}

// Disable removes the manager (and any connected member nodes) from the swarm.
// Member workers are drained off best-effort first so they don't linger as
// orphans, then the manager leaves with force (it is the last manager).
func (s *Service) Disable(ctx context.Context) error {
	if !s.CapCluster() {
		return ErrNotEnabled
	}
	// Put every workspace back on a node-local bridge FIRST. Overlays only exist
	// inside the swarm, so leaving it with workspaces still on one would strand
	// every app and database on a network that no longer exists. This is the one
	// step in Disable that must not be best-effort.
	if s.networkRollback != nil {
		if err := s.networkRollback(ctx); err != nil {
			return fmt.Errorf("could not move workspace networks back to bridges; cluster mode left enabled: %w", err)
		}
	}
	servers, err := s.nodes.List(ctx)
	if err == nil {
		s.Enrich(servers)
		for i := range servers {
			srv := &servers[i]
			if srv.IsLocal || !srv.InSwarm {
				continue
			}
			if lerr := s.LeaveNode(ctx, srv.ID, true); lerr != nil {
				logger.Warn("failed to drain node before disabling cluster", "node", srv.ID, "error", lerr)
			}
		}
	}
	if err := s.clients.Local().SwarmLeave(ctx, true); err != nil {
		return err
	}
	logger.Info("left docker swarm (cluster mode disabled)")
	s.Refresh(ctx)
	return nil
}

// JoinNode joins a worker node to the swarm via its Docker API (over the agent
// tunnel), using the worker join token and the manager's advertised address.
// Idempotent: a node already in the swarm just has its swarm id reconciled. The
// node must be online (its Docker client reachable).
func (s *Service) JoinNode(ctx context.Context, serverID uint) error {
	if !s.CapCluster() {
		return ErrNotEnabled
	}
	srv, err := s.nodes.Get(serverID)
	if err != nil {
		return err
	}
	if srv.IsLocal {
		return ErrManagerNode
	}
	dc, err := s.clients.For(serverID)
	if err != nil {
		return err // node offline
	}
	// Already a member? Reconcile its swarm id and return.
	if cur, cerr := dc.Swarm(ctx); cerr == nil && cur.LocalNodeState == swarmStateActive {
		if cur.NodeID != "" {
			_ = s.nodes.SetSwarmNodeID(serverID, cur.NodeID)
		}
		s.Refresh(ctx)
		return nil
	}
	tokens, err := s.clients.Local().SwarmJoinTokens(ctx)
	if err != nil {
		return err
	}
	remote, err := s.managerRemoteAddr()
	if err != nil {
		return err
	}
	if err := dc.SwarmJoin(ctx, docker.SwarmJoinRequest{
		RemoteAddrs: []string{remote},
		JoinToken:   tokens.Worker,
	}); err != nil {
		return err
	}
	logger.Info("joined node to swarm", "node", serverID, "name", srv.Name)
	// Read back the node's swarm id for stable correlation on the Nodes page.
	if cur, cerr := dc.Swarm(ctx); cerr == nil && cur.NodeID != "" {
		_ = s.nodes.SetSwarmNodeID(serverID, cur.NodeID)
	}
	s.Refresh(ctx)
	return nil
}

// Member is one swarm node (docker node ls), annotated with the Miabi node it
// maps to — or marked unmanaged when it is a swarm member with no Miabi record
// (e.g. a host joined by hand).
type Member struct {
	docker.SwarmNode
	// Managed is true when this swarm node maps to a Miabi node.
	Managed bool `json:"managed"`
	// ServerID / ServerName identify the mapped Miabi node (when Managed).
	ServerID   uint   `json:"server_id,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	// IsManager marks the entry that is this Miabi control-plane node.
	IsManager bool `json:"is_manager"`
}

// Members returns the swarm's nodes (docker node ls) annotated with whether each
// maps to a managed Miabi node. Empty when cluster mode is off.
func (s *Service) Members(ctx context.Context) ([]Member, error) {
	if !s.CapCluster() {
		return []Member{}, nil
	}
	list, err := s.clients.Local().SwarmNodes(ctx)
	if err != nil {
		return nil, err
	}
	servers, _ := s.nodes.List(ctx)
	bySwarmID := map[string]*models.Server{}
	for i := range servers {
		if servers[i].SwarmNodeID != "" {
			bySwarmID[servers[i].SwarmNodeID] = &servers[i]
		}
	}
	out := make([]Member, 0, len(list))
	for _, n := range list {
		m := Member{SwarmNode: n}
		srv := bySwarmID[n.ID]
		if srv == nil {
			// Fall back to a hostname match for nodes whose swarm id isn't stored.
			for i := range servers {
				if servers[i].SwarmNodeID != "" {
					continue
				}
				if strings.EqualFold(servers[i].PublicHostname, n.Hostname) || strings.EqualFold(servers[i].Name, n.Hostname) {
					srv = &servers[i]
					break
				}
			}
		}
		if srv != nil {
			m.Managed = true
			m.ServerID = srv.ID
			m.ServerName = srv.Name
			m.IsManager = srv.IsLocal
		}
		out = append(out, m)
	}
	return out, nil
}

// JoinInstructions are what an operator needs to join a host to the swarm by
// hand — used for nodes that are not reachable over the agent tunnel (offline
// managed nodes, or hosts Miabi does not manage at all). The command is run on
// the host itself.
type JoinInstructions struct {
	// WorkerToken is the swarm worker join token (a secret; admin-only).
	WorkerToken string `json:"worker_token"`
	// ManagerAddr is the manager address the host dials to join (host:port).
	ManagerAddr string `json:"manager_addr"`
	// Command is a ready-to-run `docker swarm join` command.
	Command string `json:"command"`
}

// JoinInstructions returns the manual join command + worker token, fetched live
// from the swarm (never persisted). Requires cluster mode to be enabled.
func (s *Service) JoinInstructions(ctx context.Context) (JoinInstructions, error) {
	if !s.CapCluster() {
		return JoinInstructions{}, ErrNotEnabled
	}
	tokens, err := s.clients.Local().SwarmJoinTokens(ctx)
	if err != nil {
		return JoinInstructions{}, err
	}
	remote, err := s.managerRemoteAddr()
	if err != nil {
		return JoinInstructions{}, err
	}
	return JoinInstructions{
		WorkerToken: tokens.Worker,
		ManagerAddr: remote,
		Command:     fmt.Sprintf("docker swarm join --token %s %s", tokens.Worker, remote),
	}, nil
}

// ReaffirmNode re-joins a node that Miabi already considers a swarm member (its
// swarm id is stored) if it has somehow dropped out — e.g. after the node host
// was rebuilt. It never joins a node that was never a member, so standalone /
// edge-only nodes are left alone. Best-effort; intended for the agent-connect
// hook. A no-op when cluster mode is off or the node is not a known member.
func (s *Service) ReaffirmNode(ctx context.Context, serverID uint) {
	if !s.CapCluster() {
		return
	}
	srv, err := s.nodes.Get(serverID)
	if err != nil || srv.IsLocal || srv.SwarmNodeID == "" {
		return
	}
	dc, err := s.clients.For(serverID)
	if err != nil {
		return
	}
	if cur, cerr := dc.Swarm(ctx); cerr != nil || cur.LocalNodeState == swarmStateActive {
		return // unreachable, or still a member — nothing to do
	}
	if jerr := s.JoinNode(ctx, serverID); jerr != nil {
		logger.Warn("failed to reaffirm node swarm membership", "node", serverID, "error", jerr)
	}
}

// LeaveNode removes a worker node from the swarm via its Docker API, then prunes
// it from the manager's node list so the Nodes page does not show a stale
// "down" entry. force is passed to the node-side leave.
func (s *Service) LeaveNode(ctx context.Context, serverID uint, force bool) error {
	srv, err := s.nodes.Get(serverID)
	if err != nil {
		return err
	}
	if srv.IsLocal {
		return ErrManagerNode
	}
	swarmNodeID := srv.SwarmNodeID
	if dc, derr := s.clients.For(serverID); derr == nil {
		if lerr := dc.SwarmLeave(ctx, force); lerr != nil {
			return lerr
		}
	} else if !force {
		// Node offline and not forcing: cannot leave it gracefully.
		return derr
	}
	// Remove the (now-departed) node from the manager's list, best-effort.
	if swarmNodeID != "" && s.CapCluster() {
		if rerr := s.clients.Local().SwarmNodeRemove(ctx, swarmNodeID, true); rerr != nil {
			logger.Warn("failed to remove node from swarm list", "node", serverID, "swarm_node", swarmNodeID, "error", rerr)
		}
	}
	_ = s.nodes.SetSwarmNodeID(serverID, "")
	logger.Info("removed node from swarm", "node", serverID, "name", srv.Name)
	s.Refresh(ctx)
	return nil
}

// managerRemoteAddr returns the manager address a worker dials to join, derived
// from the manager's advertised swarm address with the standard management port
// appended when absent.
func (s *Service) managerRemoteAddr() (string, error) {
	s.mu.RLock()
	addr := strings.TrimSpace(s.info.NodeAddr)
	s.mu.RUnlock()
	if addr == "" {
		return "", ErrManagerAddrUnknown
	}
	if !strings.Contains(addr, ":") {
		addr += ":2377"
	}
	return addr, nil
}
