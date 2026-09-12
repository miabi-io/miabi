// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cluster drives Docker Swarm as Miabi's optional cluster mode, one swarm per cluster. The default
// cluster follows the local engine and is auto-detected; another cluster becomes a swarm when an admin
// initializes it over its node's agent tunnel. Plain single-node Docker stays first-class.
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
	"github.com/miabi-io/miabi/internal/services/netalloc"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/slug"
)

var (
	// ErrNotEnabled is returned when a cluster operation needs a swarm the cluster does not run.
	ErrNotEnabled = errors.New("cluster mode is not enabled")
	// ErrAdvertiseAddrRequired is returned when initializing a swarm without an advertise address.
	ErrAdvertiseAddrRequired = errors.New("an advertise address is required to enable cluster mode")
	// ErrManagerNode is returned when an operation that targets a member node is pointed at a manager.
	ErrManagerNode = errors.New("the manager node cannot be used for this operation")
	// ErrManagerAddrUnknown is returned when the manager's swarm address has not been detected yet.
	ErrManagerAddrUnknown = errors.New("manager swarm address is unknown; refresh cluster state")
	// ErrClusterNotFound is returned for a cluster id with no row.
	ErrClusterNotFound = errors.New("cluster not found")
	// ErrNodeHasWorkloads is returned when a node with workloads would enter or leave a remote swarm, whose
	// workspace networks exist only as overlays.
	ErrNodeHasWorkloads = errors.New("the node still has apps, databases or volumes; a remote swarm only takes and releases empty nodes")
	// ErrClusterHasWorkloads is returned when disabling a remote swarm that still holds workloads.
	ErrClusterHasWorkloads = errors.New("the cluster still has apps, databases or volumes; move or delete them before disabling Swarm")
	// ErrNodeInOtherSwarm is returned for a node whose engine already belongs to another swarm.
	ErrNodeInOtherSwarm = errors.New("the node is already in another swarm; remove it from that swarm first")
)

const (
	swarmStateActive = "active"
	// AgentLabel marks swarm nodes Miabi already reaches directly (the local socket or their own agent), so
	// the global agent service skips them rather than opening a second tunnel for the same node.
	AgentLabel       = "miabi.agent"
	agentLabelDirect = "direct"
)

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
	SetEngineVersion(swarmNodeID, version string) error
}

// Store reads and writes cluster rows. Satisfied by repositories.ClusterRepository.
type Store interface {
	List() ([]models.Cluster, error)
	FindDefault() (*models.Cluster, error)
	FindByID(id uint) (*models.Cluster, error)
	FindByName(name string) (*models.Cluster, error)
	FindByAgentTokenHash(hash string) (*models.Cluster, error)
	IDByUID(uid string) (uint, error)
	UpdateColumns(id uint, cols map[string]any) error
	CountWorkloads(clusterID uint) (int64, error)
	CountServerWorkloadsByKind(serverID uint) (apps, databases, volumes int64, err error)
	CreateStandalone(srv *models.Server, name string) (*models.Cluster, error)
	AssignServer(serverID, clusterID uint) error
}

// swarmState is one cluster's swarm as its manager reported it at the last refresh.
type swarmState struct {
	info  docker.SwarmInfo
	nodes map[string]docker.SwarmNode
}

func (st swarmState) active() bool {
	return st.info.LocalNodeState == swarmStateActive && st.info.ControlAvailable
}

// Service tracks each cluster's swarm state and exposes cluster operations.
type Service struct {
	clients NodeDocker
	nodes   Nodes
	store   Store
	alloc   *netalloc.Service

	mu               sync.RWMutex
	states           map[uint]swarmState // by cluster id; the default cluster is kept under 0
	refreshedAt      time.Time
	defaultClusterID uint

	// ingressReconciler re-asserts the central gateway's attachment to the default cluster's ingress overlay,
	// so a gateway recreate can't leave clustered apps dark for longer than a refresh interval.
	ingressReconciler func(context.Context) error
	gatewayListener   func(context.Context, uint)

	// networkMigrator converts the default cluster's workspace bridges into overlays when Swarm is enabled,
	// networkRollback reverses it before leaving, and networkPending counts bridges still left.
	networkMigrator func(context.Context) error
	networkRollback func(context.Context) error
	networkPending  func() int

	probeImages        NetCheckImages
	probeImageFallback string

	// The global agent service (see agents.go): the registrar that turns a self-reporting
	// agent into a Miabi node, the address the agents dial back on, and the agent image.
	registrar          NodeRegistrar
	controlURL         string
	agentImages        NetCheckImages
	agentImageFallback string
}

// NewService builds the cluster service. Call Refresh once at boot to populate the swarm state.
func NewService(clients NodeDocker, nodes Nodes) *Service {
	return &Service{clients: clients, nodes: nodes, states: map[uint]swarmState{}}
}

// SetStore wires the cluster rows (nil-safe; nil leaves only the default cluster, unnamed).
func (s *Service) SetStore(st Store) { s.store = st }

// SetNetworkMigrator wires the default cluster's workspace-network conversion: `migrate` (bridge -> overlay)
// runs on Enable, `rollback` on Disable, and `pending` counts the networks still on bridges. Nil-safe.
func (s *Service) SetNetworkMigrator(migrate, rollback func(context.Context) error, pending func() int) {
	s.networkMigrator, s.networkRollback, s.networkPending = migrate, rollback, pending
}

// SetIngressReconciler wires the callback that re-asserts the central gateway's attachment to the
// default cluster's ingress overlay, run on every Refresh. Nil-safe.
func (s *Service) SetIngressReconciler(fn func(context.Context) error) {
	s.mu.Lock()
	s.ingressReconciler = fn
	s.mu.Unlock()
}

// ApplyNetworking converts the default cluster's workspace bridges into overlays on demand, for an install
// already clustered when it upgraded. A remote swarm only ever holds overlays, so there is nothing to apply.
func (s *Service) ApplyNetworking(ctx context.Context, clusterID uint) error {
	if !s.isDefault(clusterID) {
		return nil
	}
	if !s.CapCluster() {
		return ErrNotEnabled
	}
	if s.networkMigrator == nil {
		return errors.New("workspace-network migration is not wired")
	}
	return s.networkMigrator(ctx)
}

// migrateNetworks is best-effort at the call site: a failing workspace stays on its bridge rather than
// failing the whole enable.
func (s *Service) migrateNetworks(ctx context.Context) {
	if s.networkMigrator == nil {
		return
	}
	if err := s.networkMigrator(ctx); err != nil {
		logger.Warn("cluster enabled, but migrating workspace networks to overlays failed", "error", err)
	}
}

func (s *Service) isDefault(clusterID uint) bool {
	return clusterID == models.DefaultClusterID || clusterID == s.defaultID()
}

// defaultID caches the default cluster's id, which never changes once created.
func (s *Service) defaultID() uint {
	s.mu.RLock()
	id := s.defaultClusterID
	s.mu.RUnlock()
	if id != 0 || s.store == nil {
		return id
	}
	def, err := s.store.FindDefault()
	if err != nil {
		return 0
	}
	s.mu.Lock()
	s.defaultClusterID = def.ID
	s.mu.Unlock()
	return def.ID
}

func (s *Service) find(clusterID uint) (*models.Cluster, error) {
	if s.store == nil {
		if s.isDefault(clusterID) {
			return &models.Cluster{IsDefault: true}, nil
		}
		return nil, ErrClusterNotFound
	}
	c, err := s.store.FindByID(clusterID)
	if err != nil {
		return nil, ErrClusterNotFound
	}
	return c, nil
}

func (s *Service) inCluster(srv *models.Server, clusterID uint) bool {
	if s.isDefault(clusterID) {
		return s.isDefault(srv.ClusterID)
	}
	return srv.ClusterID == clusterID
}

func (s *Service) state(clusterID uint) swarmState {
	key := clusterID
	if s.isDefault(clusterID) {
		key = models.DefaultClusterID
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.states[key]
}

// CapCluster reports whether the default cluster's engine is a reachable swarm manager.
func (s *Service) CapCluster() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.states[models.DefaultClusterID].active()
}

// IsSwarm reports whether a cluster runs a swarm the control plane can drive right now.
func (s *Service) IsSwarm(clusterID uint) bool {
	return s.state(clusterID).active()
}

// AnySwarm reports whether any cluster runs a swarm, which is when the service runtime can be offered.
func (s *Service) AnySwarm() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, st := range s.states {
		if st.active() {
			return true
		}
	}
	return false
}

// Manager returns a Docker client that can issue Swarm and engine calls for the cluster: the local socket
// for the default cluster, the node itself for a standalone one, and a manager's tunnel for a remote swarm.
func (s *Service) Manager(_ context.Context, clusterID uint) (docker.Client, error) {
	if s.isDefault(clusterID) {
		return s.clients.Local(), nil
	}
	c, err := s.find(clusterID)
	if err != nil {
		return nil, err
	}
	if c.Mode != models.ClusterModeSwarm {
		return s.clients.For(c.ManagerServerID)
	}
	return s.remoteManager(c)
}

// remoteManager reaches a remote swarm through its preferred manager, or through any other manager
// of the cluster whose agent is connected.
func (s *Service) remoteManager(c *models.Cluster) (docker.Client, error) {
	dc, err := s.clients.For(c.ManagerServerID)
	if err == nil {
		return dc, nil
	}
	st := s.state(c.ID)
	servers, lerr := s.nodes.List(context.Background())
	if lerr != nil {
		return nil, err
	}
	for i := range servers {
		srv := &servers[i]
		if srv.ClusterID != c.ID || srv.ID == c.ManagerServerID || srv.SwarmNodeID == "" {
			continue
		}
		if n, ok := st.nodes[srv.SwarmNodeID]; ok && n.Role == "manager" {
			if alt, aerr := s.clients.For(srv.ID); aerr == nil {
				return alt, nil
			}
		}
	}
	return nil, err
}

// Status is a cluster's swarm status as surfaced to the console.
type Status struct {
	// Enabled reports whether the cluster runs a swarm the control plane can drive.
	Enabled bool `json:"enabled"`
	// Name is the cluster's display name.
	Name string `json:"name,omitempty"`
	// LocalNodeState is the manager engine's swarm state (inactive on plain Docker).
	LocalNodeState string `json:"local_node_state"`
	// ManagerAddr is the address the manager advertises to swarm peers.
	ManagerAddr string `json:"manager_addr,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Managers    int    `json:"managers"`
	Nodes       int    `json:"nodes"`
	// IngressNetwork is the attachable overlay a gateway joins to reach service VIPs; set only in cluster mode.
	IngressNetwork string `json:"ingress_network,omitempty"`
	// NetworksPending counts the default cluster's workspace networks still on node-local bridges, which
	// have no cross-node connectivity until cluster networking is applied.
	NetworksPending int `json:"networks_pending,omitempty"`
	// AgentsDeployed reports whether the global agent service is installed, i.e. whether swarm members
	// are managed (metrics, stats, shell, housekeeping) or merely run tasks Miabi cannot see into.
	AgentsDeployed bool `json:"agents_deployed"`
	AgentTasks     int  `json:"agent_tasks,omitempty"`
	// AgentInsecureTLS is true when those agents skip verification of the control plane's certificate.
	AgentInsecureTLS bool `json:"agent_insecure_tls,omitempty"`
	// AgentCustomCA is true when the agents verify against an operator-supplied CA.
	AgentCustomCA bool `json:"agent_custom_ca,omitempty"`
	// AgentCACertPath is set when that CA is a file that must exist on every node.
	AgentCACertPath string `json:"agent_ca_cert_path,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Status returns a cluster's last-refreshed swarm status.
func (s *Service) Status(clusterID uint) Status {
	st := s.state(clusterID)
	out := Status{
		Enabled:        st.active(),
		LocalNodeState: st.info.LocalNodeState,
		ManagerAddr:    st.info.NodeAddr,
		NodeID:         st.info.NodeID,
		Managers:       st.info.Managers,
		Nodes:          st.info.Nodes,
		Error:          st.info.Error,
	}
	if s.isDefault(clusterID) {
		out.Name = s.Name()
		if out.Enabled && s.networkPending != nil {
			out.NetworksPending = s.networkPending()
		}
	} else if c, err := s.find(clusterID); err == nil {
		out.Name = c.Label()
	}
	if out.Enabled {
		out.IngressNetwork = node.IngressOverlay
	}
	return out
}

// Refresh re-reads every swarm cluster's state from its manager. Cheap and safe on plain Docker.
// Called at boot and on an interval.
func (s *Service) Refresh(ctx context.Context) {
	local := s.clients.Local()
	info, err := local.Swarm(ctx)
	if err != nil {
		logger.Warn("failed to read swarm state", "error", err)
		info = docker.SwarmInfo{}
	}
	states := s.refreshRemote(ctx)
	states[models.DefaultClusterID] = s.readSwarm(ctx, local, info)

	s.mu.Lock()
	s.states = states
	s.refreshedAt = time.Now()
	ingress := s.ingressReconciler
	s.mu.Unlock()
	s.syncDefaultMode(info)
	s.reconcileMembership(ctx, states)
	for id, st := range states {
		if id != models.DefaultClusterID && st.active() {
			s.AttachGateway(ctx, id)
		}
	}

	if info.NodeID != "" {
		if id := s.clients.LocalID(); id != 0 {
			if serr := s.nodes.SetSwarmNodeID(id, info.NodeID); serr != nil {
				logger.Warn("failed to persist manager swarm node id", "error", serr)
			}
		}
	}
	if ingress != nil && info.ControlAvailable {
		if err := ingress(ctx); err != nil {
			logger.Warn("failed to reconcile cluster ingress gateway", "error", err)
		}
	}
}

func (s *Service) readSwarm(ctx context.Context, mgr docker.Client, info docker.SwarmInfo) swarmState {
	st := swarmState{info: info, nodes: map[string]docker.SwarmNode{}}
	if !info.ControlAvailable {
		return st
	}
	list, err := mgr.SwarmNodes(ctx)
	if err != nil {
		logger.Warn("failed to list swarm nodes", "error", err)
		return st
	}
	for _, n := range list {
		st.nodes[n.ID] = n
		// From the manager's view, so a daemon too old for the SDK is flagged even without a client for it.
		if n.EngineVersion != "" {
			if serr := s.nodes.SetEngineVersion(n.ID, n.EngineVersion); serr != nil {
				logger.Warn("failed to persist node engine version", "swarm_node_id", n.ID, "error", serr)
			}
		}
	}
	return st
}

func (s *Service) refreshRemote(ctx context.Context) map[uint]swarmState {
	out := map[uint]swarmState{}
	if s.store == nil {
		return out
	}
	clusters, err := s.store.List()
	if err != nil {
		logger.Warn("failed to list clusters", "error", err)
		return out
	}
	for i := range clusters {
		c := &clusters[i]
		if c.IsDefault || c.Mode != models.ClusterModeSwarm {
			continue
		}
		mgr, err := s.remoteManager(c)
		if err != nil {
			out[c.ID] = swarmState{info: docker.SwarmInfo{Error: "no manager of this cluster is connected"}}
			continue
		}
		info, err := mgr.Swarm(ctx)
		if err != nil {
			out[c.ID] = swarmState{info: docker.SwarmInfo{Error: err.Error()}}
			continue
		}
		out[c.ID] = s.readSwarm(ctx, mgr, info)
	}
	return out
}

// reconcileMembership moves a node into the cluster whose swarm it is actually in, however it got there:
// a join from the console, a `docker swarm join` by hand, or an agent the swarm brought in.
func (s *Service) reconcileMembership(ctx context.Context, states map[uint]swarmState) {
	if s.store == nil {
		return
	}
	servers, err := s.nodes.List(ctx)
	if err != nil {
		return
	}
	for i := range servers {
		srv := &servers[i]
		if srv.IsLocal || srv.SwarmNodeID == "" {
			continue
		}
		for key, st := range states {
			if _, ok := st.nodes[srv.SwarmNodeID]; !ok {
				continue
			}
			target := key
			if key == models.DefaultClusterID {
				target = s.defaultID()
			}
			if target != 0 && srv.ClusterID != target {
				if err := s.store.AssignServer(srv.ID, target); err != nil {
					logger.Warn("failed to move a swarm member into its cluster", "node", srv.Name, "cluster", target, "error", err)
				} else {
					logger.Info("moved a swarm member into its cluster", "node", srv.Name, "cluster", target)
				}
			}
			break
		}
	}
}

// syncDefaultMode records whether the default cluster runs a swarm, which the local engine decides: the
// default cluster's mode stays auto-detected rather than configured.
func (s *Service) syncDefaultMode(info docker.SwarmInfo) {
	if s.store == nil {
		return
	}
	def, err := s.store.FindDefault()
	if err != nil {
		return
	}
	mode := models.ClusterModeStandalone
	if info.LocalNodeState == swarmStateActive && info.ControlAvailable {
		mode = models.ClusterModeSwarm
	}
	if def.Mode != mode {
		if err := s.store.UpdateColumns(def.ID, map[string]any{"mode": mode}); err != nil {
			logger.Warn("failed to record the default cluster's mode", "mode", mode, "error", err)
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

// Enrich annotates each server's transient swarm fields from its cluster's last refresh. A node outside
// every swarm shows as standalone while any cluster runs one.
func (s *Service) Enrich(servers []models.Server) {
	s.mu.RLock()
	states := s.states
	s.mu.RUnlock()
	anyActive := false
	for _, st := range states {
		anyActive = anyActive || st.active()
	}
	for i := range servers {
		srv := &servers[i]
		key := srv.ClusterID
		if s.isDefault(key) {
			key = models.DefaultClusterID
		}
		st, ok := states[key]
		if !ok || !st.active() {
			if anyActive {
				srv.SwarmRole = "standalone"
			}
			continue
		}
		id := srv.SwarmNodeID
		if id == "" && srv.IsLocal {
			id = st.info.NodeID
		}
		n, found := st.nodes[id]
		if !found || id == "" {
			n, found = matchByHostname(st.nodes, srv)
		}
		if !found {
			srv.SwarmRole = "standalone"
			continue
		}
		srv.InSwarm = true
		srv.SwarmRole = roleOf(n)
		srv.SwarmAvailability = n.Availability
		srv.SwarmState = n.State
	}
}

// roleOf maps a swarm node to the role shown in the UI: "leader", "manager" or "worker".
func roleOf(n docker.SwarmNode) string {
	if n.Leader {
		return "leader"
	}
	if n.Role != "" {
		return n.Role
	}
	return "worker"
}

// matchByHostname is the fallback correlation when a node's swarm id is not yet stored. The label is
// often the machine's hostname while the handle is slugified from it, so both are tried.
func matchByHostname(swarmNodes map[string]docker.SwarmNode, srv *models.Server) (docker.SwarmNode, bool) {
	candidates := []string{srv.PublicHostname, srv.DisplayName, srv.Name}
	for _, n := range swarmNodes {
		for _, c := range candidates {
			if c != "" && strings.EqualFold(n.Hostname, c) {
				return n, true
			}
		}
	}
	return docker.SwarmNode{}, false
}

// Enable makes a cluster a swarm. The default cluster initializes (or adopts) the local engine's swarm; any
// other cluster initializes one on its node, over the agent tunnel. advertiseAddr is the address peers
// reach the manager on, ignored when adopting a swarm the engine is already managing.
func (s *Service) Enable(ctx context.Context, clusterID uint, advertiseAddr string) (Status, error) {
	if s.isDefault(clusterID) {
		return s.enableDefault(ctx, advertiseAddr)
	}
	c, err := s.find(clusterID)
	if err != nil {
		return Status{}, err
	}
	return s.enableRemote(ctx, c, advertiseAddr)
}

func (s *Service) enableDefault(ctx context.Context, advertiseAddr string) (Status, error) {
	local := s.clients.Local()
	info, err := local.Swarm(ctx)
	if err != nil {
		return Status{}, err
	}
	if info.LocalNodeState == swarmStateActive {
		s.Refresh(ctx)
		if !s.CapCluster() {
			return Status{}, errors.New("docker is in swarm mode but this engine is not a reachable manager")
		}
		labelDirect(ctx, local, info.NodeID)
		ensureIngressOverlay(ctx, local)
		s.migrateNetworks(ctx)
		logger.Info("adopted existing docker swarm", "node_id", info.NodeID)
		return s.Status(models.DefaultClusterID), nil
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
	labelDirect(ctx, local, nodeID)
	ensureIngressOverlay(ctx, local)
	// Refresh first: the migration refuses to run until CapCluster() is true.
	s.migrateNetworks(ctx)
	return s.Status(models.DefaultClusterID), nil
}

func (s *Service) enableRemote(ctx context.Context, c *models.Cluster, advertiseAddr string) (Status, error) {
	if c.Mode == models.ClusterModeSwarm {
		s.Refresh(ctx)
		return s.Status(c.ID), nil
	}
	if err := s.requireEmptyNode(c.ManagerServerID, EnableSwarmAction); err != nil {
		return Status{}, err
	}
	dc, err := s.clients.For(c.ManagerServerID)
	if err != nil {
		return Status{}, err
	}
	info, err := dc.Swarm(ctx)
	if err != nil {
		return Status{}, err
	}
	nodeID := info.NodeID
	switch {
	case info.LocalNodeState == swarmStateActive && !info.ControlAvailable:
		return Status{}, ErrNodeInOtherSwarm
	case info.LocalNodeState != swarmStateActive:
		addr := strings.TrimSpace(advertiseAddr)
		if addr == "" {
			return Status{}, ErrAdvertiseAddrRequired
		}
		if nodeID, err = dc.SwarmInit(ctx, docker.SwarmInitRequest{AdvertiseAddr: addr}); err != nil {
			return Status{}, err
		}
		logger.Info("initialized docker swarm", "cluster", c.Name, "node_id", nodeID, "advertise", addr)
	default:
		logger.Info("adopted existing docker swarm", "cluster", c.Name, "node_id", nodeID)
	}
	_ = s.nodes.SetSwarmNodeID(c.ManagerServerID, nodeID)
	labelDirect(ctx, dc, nodeID)
	ensureIngressOverlay(ctx, dc)
	if err := s.store.UpdateColumns(c.ID, map[string]any{
		"mode":              models.ClusterModeSwarm,
		"ingress_server_id": c.ManagerServerID,
	}); err != nil {
		return Status{}, err
	}
	s.Refresh(ctx)
	return s.Status(c.ID), nil
}

// ensureIngressOverlay pre-creates the swarm's ingress overlay so a gateway has a network to join before
// the first clustered app deploys. Best-effort: the deploy path creates it too.
func ensureIngressOverlay(ctx context.Context, mgr docker.Client) {
	if _, err := mgr.CreateOverlayNetwork(ctx, node.IngressOverlay); err != nil {
		logger.Warn("failed to ensure cluster ingress overlay", "network", node.IngressOverlay, "error", err)
	}
}

func labelDirect(ctx context.Context, mgr docker.Client, swarmNodeID string) {
	if swarmNodeID == "" {
		return
	}
	if err := mgr.SwarmNodeSetLabel(ctx, swarmNodeID, AgentLabel, agentLabelDirect); err != nil {
		logger.Warn("failed to label a directly connected swarm node", "swarm_node_id", swarmNodeID, "error", err)
	}
}

// Disable takes a cluster out of swarm mode. Its member nodes leave first and each becomes a standalone
// cluster of its own; the manager leaves last.
func (s *Service) Disable(ctx context.Context, clusterID uint) error {
	if s.isDefault(clusterID) {
		return s.disableDefault(ctx)
	}
	c, err := s.find(clusterID)
	if err != nil {
		return err
	}
	return s.disableRemote(ctx, c)
}

func (s *Service) disableDefault(ctx context.Context) error {
	if !s.CapCluster() {
		return ErrNotEnabled
	}
	// Overlays die with the swarm, so every workspace must be back on a bridge first or it would be
	// stranded on a network that no longer exists. The one step that must not be best-effort.
	if s.networkRollback != nil {
		if err := s.networkRollback(ctx); err != nil {
			return fmt.Errorf("could not move workspace networks back to bridges; cluster mode left enabled: %w", err)
		}
	}
	s.leaveMembers(ctx, models.DefaultClusterID, 0)
	if err := s.clients.Local().SwarmLeave(ctx, true); err != nil {
		return err
	}
	logger.Info("left docker swarm (cluster mode disabled)")
	s.Refresh(ctx)
	return nil
}

func (s *Service) disableRemote(ctx context.Context, c *models.Cluster) error {
	if !s.IsSwarm(c.ID) {
		return ErrNotEnabled
	}
	n, err := s.store.CountWorkloads(c.ID)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrClusterHasWorkloads
	}
	dc, err := s.clients.For(c.ManagerServerID)
	if err != nil {
		return err
	}
	s.leaveMembers(ctx, c.ID, c.ManagerServerID)
	if err := dc.SwarmLeave(ctx, true); err != nil {
		return err
	}
	_ = s.nodes.SetSwarmNodeID(c.ManagerServerID, "")
	if err := s.store.UpdateColumns(c.ID, map[string]any{
		"mode":             models.ClusterModeStandalone,
		"agent_token_hash": "",
	}); err != nil {
		return err
	}
	logger.Info("left docker swarm", "cluster", c.Name)
	s.Refresh(ctx)
	return nil
}

func (s *Service) leaveMembers(ctx context.Context, clusterID, managerServerID uint) {
	servers, err := s.nodes.List(ctx)
	if err != nil {
		return
	}
	s.Enrich(servers)
	for i := range servers {
		srv := &servers[i]
		if srv.IsLocal || srv.ID == managerServerID || !srv.InSwarm || !s.inCluster(srv, clusterID) {
			continue
		}
		if lerr := s.LeaveNode(ctx, srv.ID, true); lerr != nil {
			logger.Warn("failed to remove a node before disabling the swarm", "node", srv.ID, "error", lerr)
		}
	}
}

// JoinNode joins a node to a cluster's swarm over its agent tunnel, with the worker join token and the
// manager's advertised address, then moves the node and everything placed on it into that cluster.
// Idempotent: a node already in this swarm just has its swarm id reconciled.
func (s *Service) JoinNode(ctx context.Context, clusterID, serverID uint) error {
	if !s.IsSwarm(clusterID) {
		return ErrNotEnabled
	}
	c, err := s.find(clusterID)
	if err != nil {
		return err
	}
	srv, err := s.nodes.Get(serverID)
	if err != nil {
		return err
	}
	if srv.IsLocal || (!c.IsDefault && srv.ID == c.ManagerServerID) {
		return ErrManagerNode
	}
	moving := !s.inCluster(srv, clusterID)
	if moving {
		if s.IsSwarm(srv.ClusterID) {
			return ErrNodeInOtherSwarm
		}
		if !c.IsDefault {
			if err := s.requireEmptyNode(serverID, JoinAction); err != nil {
				return err
			}
		}
	}
	dc, err := s.clients.For(serverID)
	if err != nil {
		return err
	}
	mgr, err := s.Manager(ctx, clusterID)
	if err != nil {
		return err
	}
	cur, cerr := dc.Swarm(ctx)
	if cerr == nil && cur.LocalNodeState == swarmStateActive {
		if !isMember(ctx, mgr, cur.NodeID) {
			return ErrNodeInOtherSwarm
		}
	} else {
		tokens, err := mgr.SwarmJoinTokens(ctx)
		if err != nil {
			return err
		}
		remote, err := s.managerRemoteAddr(clusterID)
		if err != nil {
			return err
		}
		if err := dc.SwarmJoin(ctx, docker.SwarmJoinRequest{RemoteAddrs: []string{remote}, JoinToken: tokens.Worker}); err != nil {
			return err
		}
		logger.Info("joined node to swarm", "cluster", c.Name, "node", serverID, "name", srv.Name)
		cur, cerr = dc.Swarm(ctx)
	}
	if cerr == nil && cur.NodeID != "" {
		_ = s.nodes.SetSwarmNodeID(serverID, cur.NodeID)
		labelDirect(ctx, mgr, cur.NodeID)
	}
	if moving && s.store != nil && c.ID != 0 {
		if err := s.store.AssignServer(serverID, c.ID); err != nil {
			return fmt.Errorf("the node joined the swarm, but could not be moved into the cluster: %w", err)
		}
	}
	s.Refresh(ctx)
	return nil
}

func isMember(ctx context.Context, mgr docker.Client, swarmNodeID string) bool {
	if swarmNodeID == "" {
		return false
	}
	nodes, err := mgr.SwarmNodes(ctx)
	if err != nil {
		return false
	}
	for _, n := range nodes {
		if n.ID == swarmNodeID {
			return true
		}
	}
	return false
}

// Member is one swarm node (docker node ls), annotated with the Miabi node it maps to, or marked
// unmanaged when it has no Miabi record (e.g. a host joined by hand).
type Member struct {
	docker.SwarmNode
	// Managed is true when this swarm node maps to a Miabi node.
	Managed bool `json:"managed"`
	// ServerID / ServerName identify the mapped Miabi node (when Managed).
	ServerID   uint   `json:"server_id,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	// IsManager marks the node the control plane drives this swarm through.
	IsManager bool `json:"is_manager"`
}

// Members returns a cluster's swarm nodes annotated with the Miabi node each maps to. Empty when the
// cluster runs no swarm.
func (s *Service) Members(ctx context.Context, clusterID uint) ([]Member, error) {
	if !s.IsSwarm(clusterID) {
		return []Member{}, nil
	}
	c, err := s.find(clusterID)
	if err != nil {
		return nil, err
	}
	mgr, err := s.Manager(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	list, err := mgr.SwarmNodes(ctx)
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
			for i := range servers {
				if servers[i].SwarmNodeID != "" {
					continue
				}
				if strings.EqualFold(servers[i].PublicHostname, n.Hostname) ||
					strings.EqualFold(servers[i].DisplayName, n.Hostname) ||
					strings.EqualFold(servers[i].Name, n.Hostname) {
					srv = &servers[i]
					break
				}
			}
		}
		if srv != nil {
			m.Managed = true
			m.ServerID = srv.ID
			m.ServerName = srv.Label()
			m.IsManager = (c.IsDefault && srv.IsLocal) || (!c.IsDefault && srv.ID == c.ManagerServerID)
		}
		out = append(out, m)
	}
	return out, nil
}

// ErrInvalidAvailability is returned for an availability outside active/pause/drain.
var ErrInvalidAvailability = errors.New("availability must be active, pause or drain")

// SetAvailability changes a swarm node's scheduling availability: active places new tasks, pause keeps
// existing tasks running without placing more, and drain reschedules existing tasks off the node.
func (s *Service) SetAvailability(ctx context.Context, clusterID uint, swarmNodeID, availability string) error {
	if !s.IsSwarm(clusterID) {
		return ErrNotEnabled
	}
	switch availability {
	case "active", "pause", "drain":
	default:
		return ErrInvalidAvailability
	}
	mgr, err := s.Manager(ctx, clusterID)
	if err != nil {
		return err
	}
	if err := mgr.SwarmNodeAvailability(ctx, swarmNodeID, availability); err != nil {
		return err
	}
	logger.Info("swarm node availability changed", "node", swarmNodeID, "availability", availability)
	s.Refresh(ctx)
	return nil
}

// Tasks lists the service tasks the scheduler placed on a swarm node, or on all nodes when swarmNodeID is
// empty. Only a manager can answer this, which is the only way to see an unmanaged node's workload.
func (s *Service) Tasks(ctx context.Context, clusterID uint, swarmNodeID string) ([]docker.SwarmTask, error) {
	if !s.IsSwarm(clusterID) {
		return []docker.SwarmTask{}, nil
	}
	mgr, err := s.Manager(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	return mgr.SwarmTasks(ctx, swarmNodeID)
}

// JoinInstructions are what an operator needs to join a host to a swarm by hand, for a node not reachable
// over the agent tunnel. The command runs on the host itself.
type JoinInstructions struct {
	// WorkerToken is the swarm worker join token (a secret; admin-only).
	WorkerToken string `json:"worker_token"`
	// ManagerAddr is the manager address the host dials to join (host:port).
	ManagerAddr string `json:"manager_addr"`
	// Command is a ready-to-run `docker swarm join` command.
	Command string `json:"command"`
}

// JoinInstructions returns a cluster's manual join command, fetched live from the swarm (never persisted).
func (s *Service) JoinInstructions(ctx context.Context, clusterID uint) (JoinInstructions, error) {
	if !s.IsSwarm(clusterID) {
		return JoinInstructions{}, ErrNotEnabled
	}
	mgr, err := s.Manager(ctx, clusterID)
	if err != nil {
		return JoinInstructions{}, err
	}
	tokens, err := mgr.SwarmJoinTokens(ctx)
	if err != nil {
		return JoinInstructions{}, err
	}
	remote, err := s.managerRemoteAddr(clusterID)
	if err != nil {
		return JoinInstructions{}, err
	}
	return JoinInstructions{
		WorkerToken: tokens.Worker,
		ManagerAddr: remote,
		Command:     fmt.Sprintf("docker swarm join --token %s %s", tokens.Worker, remote),
	}, nil
}

// ReaffirmNode re-joins a node Miabi already considers a swarm member if it has dropped out, e.g. after
// the host was rebuilt. It never joins a node that was never a member. Best-effort, for the agent-connect hook.
func (s *Service) ReaffirmNode(ctx context.Context, serverID uint) {
	srv, err := s.nodes.Get(serverID)
	if err != nil || srv.IsLocal || srv.SwarmNodeID == "" || !s.IsSwarm(srv.ClusterID) {
		return
	}
	dc, err := s.clients.For(serverID)
	if err != nil {
		return
	}
	if cur, cerr := dc.Swarm(ctx); cerr != nil || cur.LocalNodeState == swarmStateActive {
		return
	}
	if jerr := s.JoinNode(ctx, srv.ClusterID, serverID); jerr != nil {
		logger.Warn("failed to reaffirm node swarm membership", "node", serverID, "error", jerr)
	}
}

// LeaveNode removes a member node from its cluster's swarm, prunes it from the manager's node list, and
// gives the node a standalone cluster of its own. force is passed to the node-side leave.
func (s *Service) LeaveNode(ctx context.Context, serverID uint, force bool) error {
	srv, err := s.nodes.Get(serverID)
	if err != nil {
		return err
	}
	if srv.IsLocal {
		return ErrManagerNode
	}
	c, cerr := s.find(srv.ClusterID)
	if cerr == nil && !c.IsDefault {
		if srv.ID == c.ManagerServerID {
			return ErrManagerNode
		}
		if err := s.requireEmptyNode(serverID, LeaveAction); err != nil {
			return err
		}
	}
	wasSwarm := s.IsSwarm(srv.ClusterID)
	swarmNodeID := srv.SwarmNodeID
	if dc, derr := s.clients.For(serverID); derr == nil {
		if lerr := dc.SwarmLeave(ctx, force); lerr != nil {
			return lerr
		}
	} else if !force {
		return derr
	}
	if swarmNodeID != "" && wasSwarm {
		if mgr, merr := s.Manager(ctx, srv.ClusterID); merr == nil {
			if rerr := mgr.SwarmNodeRemove(ctx, swarmNodeID, true); rerr != nil {
				logger.Warn("failed to remove node from swarm list", "node", serverID, "swarm_node", swarmNodeID, "error", rerr)
			}
		}
	}
	_ = s.nodes.SetSwarmNodeID(serverID, "")
	if wasSwarm {
		s.giveStandaloneCluster(srv)
	}
	logger.Info("removed node from swarm", "node", serverID, "name", srv.Name)
	s.Refresh(ctx)
	return nil
}

func (s *Service) giveStandaloneCluster(srv *models.Server) {
	if s.store == nil {
		return
	}
	name, err := slug.Unique(srv.Name, "node", func(c string) (bool, error) {
		_, ferr := s.store.FindByName(c)
		return ferr == nil, nil
	})
	if err == nil {
		_, err = s.store.CreateStandalone(srv, name)
	}
	if err != nil {
		logger.Warn("the node left the swarm, but could not get a standalone cluster", "node", srv.ID, "error", err)
	}
}

// managerRemoteAddr returns the address a node dials to join a cluster's swarm, with the standard
// management port appended when absent.
func (s *Service) managerRemoteAddr(clusterID uint) (string, error) {
	addr := strings.TrimSpace(s.state(clusterID).info.NodeAddr)
	if addr == "" {
		return "", ErrManagerAddrUnknown
	}
	if !strings.Contains(addr, ":") {
		addr += ":2377"
	}
	return addr, nil
}
