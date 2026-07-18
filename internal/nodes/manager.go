// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package nodes

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/wstunnel"
)

// Manager accepts agent tunnels and maintains the live remote Docker clients in
// the registry, plus each node's online/offline status.
type Manager struct {
	clients *Clients
	nodes   *node.Service

	mu        sync.Mutex
	sessions  map[uint]*yamux.Session
	onConnect func(ctx context.Context, srv *models.Server, token string, dc docker.Client)
	onRemove  func(ctx context.Context, srv *models.Server, dc docker.Client)
	onStatus  func(nodeID uint, name string, online bool)
	subscribe func(ctx context.Context, nodeID uint, dc docker.Client)
}

func NewManager(clients *Clients, nodes *node.Service) *Manager {
	return &Manager{clients: clients, nodes: nodes, sessions: map[uint]*yamux.Session{}}
}

// SetOnConnect registers a hook run (in a goroutine) once a node's agent has
// connected and its remote Docker client is live — e.g. to deploy an edge-gateway
// node's gateway. The hook receives the node's plaintext join token, which is
// only available at connect time.
func (m *Manager) SetOnConnect(fn func(ctx context.Context, srv *models.Server, token string, dc docker.Client)) {
	m.onConnect = fn
}

// SetSubscriber registers a per-node worker run (in a goroutine) for the node's
// whole connection lifetime: started once its Docker client is live, its context
// cancelled when the tunnel drops. Used for the per-node app event subscriber.
func (m *Manager) SetSubscriber(fn func(ctx context.Context, nodeID uint, dc docker.Client)) {
	m.subscribe = fn
}

// SetOnRemove registers a hook run when a node is being removed while still
// connected — e.g. to tear down an edge-gateway node's gateway. It runs with the
// node's live Docker client before the tunnel is closed. See Teardown.
func (m *Manager) SetOnRemove(fn func(ctx context.Context, srv *models.Server, dc docker.Client)) {
	m.onRemove = fn
}

// SetOnStatusChange registers a hook fired when a node comes online (agent
// connected) or goes offline (tunnel dropped). Used by alerting to raise/resolve
// a "node offline" alert. Best-effort: run in a goroutine, must not block.
func (m *Manager) SetOnStatusChange(fn func(nodeID uint, name string, online bool)) {
	m.onStatus = fn
}

// Teardown runs the onRemove hook with the node's live Docker client, if the
// node is currently connected. Call it before Disconnect/DeleteNode so node-side
// infrastructure can be cleaned up over the still-open tunnel. Best-effort: a
// no-op when the node is offline or no hook is set.
func (m *Manager) Teardown(ctx context.Context, srv *models.Server) {
	if m.onRemove == nil || srv == nil || !m.clients.Connected(srv.ID) {
		return
	}
	dc, err := m.clients.For(srv.ID)
	if err != nil {
		return
	}
	m.onRemove(ctx, srv, dc)
}

// Clients returns the per-node Docker client registry (for service wiring).
func (m *Manager) Clients() *Clients { return m.clients }

// Handle owns an authenticated agent WebSocket: it builds the tunnel, registers
// the node's remote Docker client, marks it online, and blocks until the tunnel
// closes (so the HTTP handler keeps the connection open). The caller has already
// validated the join token; the plaintext token is passed through so the connect
// hook can configure node-side infrastructure (e.g. the gateway).
func (m *Manager) Handle(srv *models.Server, token, agentVersion, agentContainerID string, ws *websocket.Conn) {
	nodeID := srv.ID
	sess, err := wstunnel.Client(ws) // control plane opens streams (one per Docker request)
	if err != nil {
		logger.Error("failed to start node tunnel", "node", nodeID, "error", err)
		_ = ws.Close()
		return
	}
	dcli, err := docker.NewRemote(func(_ context.Context) (net.Conn, error) {
		return sess.OpenStream()
	})
	if err != nil {
		_ = sess.Close()
		return
	}

	m.replace(nodeID, sess)
	m.clients.SetRemote(nodeID, dcli)
	m.clients.SetRemoteSelf(nodeID, agentContainerID) // protect the agent's own container from removal
	m.nodes.MarkConnected(nodeID, agentVersion)
	logger.Info("node agent connected", "node", nodeID, "name", srv.Name, "agent", agentVersion)
	if m.onStatus != nil {
		go m.onStatus(nodeID, srv.Name, true)
	}

	// Connection-scoped context: cancelled when the tunnel drops, so per-node
	// workers (event subscriber) stop with their node.
	connCtx, cancelConn := context.WithCancel(context.Background())

	if m.onConnect != nil {
		go m.onConnect(connCtx, srv, token, dcli)
	}
	if m.subscribe != nil {
		go m.subscribe(connCtx, nodeID, dcli)
	}

	// Refresh last-seen periodically while connected.
	stop := make(chan struct{})
	go m.heartbeat(nodeID, agentVersion, stop)

	<-sess.CloseChan() // blocks until the tunnel dies (keepalive timeout or close)
	cancelConn()
	close(stop)

	// Only tear down the shared client registry and node status if THIS connection
	// is still the current one. On a fast reconnect, replace() has already
	// installed the new session/client and marked the node connected; without this
	// guard the superseded goroutine would rip out the live client and flip a
	// connected node to offline. forget() deletes-and-reports under the same lock.
	if m.forget(nodeID, sess) {
		m.clients.RemoveRemote(nodeID)
		m.nodes.MarkDisconnected(nodeID)
		logger.Info("node agent disconnected", "node", nodeID, "name", srv.Name)
		if m.onStatus != nil {
			go m.onStatus(nodeID, srv.Name, false)
		}
		return
	}
	logger.Info("superseded node tunnel closed; keeping the live connection", "node", nodeID, "name", srv.Name)
}

// Connected reports whether a node currently has a live agent tunnel.
func (m *Manager) Connected(nodeID uint) bool { return m.clients.Connected(nodeID) }

// nodeHealthProbeTimeout bounds a single Docker ping over a node's tunnel. A
// healthy daemon answers in milliseconds; only a dead / half-open tunnel takes
// this long, so it's generous enough to avoid tearing down a momentarily-slow
// node.
const nodeHealthProbeTimeout = 8 * time.Second

// probe pings a node's Docker daemon over its tunnel within the health timeout.
// A non-nil result means the tunnel is dead or half-open.
func (m *Manager) probe(ctx context.Context, nodeID uint) error {
	dc, err := m.clients.For(nodeID)
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, nodeHealthProbeTimeout)
	defer cancel()
	return dc.Ping(pctx)
}

// ReconcileHealth actively verifies every connected node by pinging its Docker
// daemon over the tunnel. A node that fails to answer has a dead or half-open
// tunnel that yamux keepalive hasn't caught yet, so its session is closed —
// which triggers the normal disconnect cleanup (registry removal +
// MarkDisconnected), correcting a stale "online" status. Run periodically by the
// node-health cron task as a global backstop to the per-connection heartbeat;
// safe to call concurrently with real Docker traffic (ping opens its own
// short-lived stream).
func (m *Manager) ReconcileHealth(ctx context.Context) {
	for _, nodeID := range m.clients.RemoteIDs() {
		if err := m.probe(ctx, nodeID); err != nil {
			logger.Warn("node health probe failed; tearing down stale tunnel", "node", nodeID, "error", err)
			m.Disconnect(nodeID) // closes the session → Handle unblocks and cleans up
		}
	}
}

// Disconnect tears down a node's tunnel (e.g. when it is deleted).
func (m *Manager) Disconnect(nodeID uint) {
	m.mu.Lock()
	sess := m.sessions[nodeID]
	m.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

// heartbeat probes the node's tunnel every 30s while connected: on success it
// refreshes LastSeenAt (so that timestamp reflects a real liveness check, not a
// blind timer); on failure it tears the tunnel down, so a node that dropped
// ungracefully self-heals to "offline" within ~30s rather than lingering online.
func (m *Manager) heartbeat(nodeID uint, agentVersion string, stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := m.probe(context.Background(), nodeID); err != nil {
				logger.Warn("node heartbeat probe failed; tearing down tunnel", "node", nodeID, "error", err)
				m.Disconnect(nodeID)
				return
			}
			m.nodes.MarkConnected(nodeID, agentVersion)
		}
	}
}

// replace registers the new session, closing any prior one for the node (a
// reconnect supersedes a stale tunnel).
func (m *Manager) replace(nodeID uint, sess *yamux.Session) {
	m.mu.Lock()
	old := m.sessions[nodeID]
	m.sessions[nodeID] = sess
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// forget removes the node's session iff sess is still the current one, and
// reports whether it did — i.e. whether this connection was the live tunnel. A
// superseded connection (already replaced by a reconnect) returns false so its
// goroutine skips the shared disconnect cleanup.
func (m *Manager) forget(nodeID uint, sess *yamux.Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[nodeID] == sess {
		delete(m.sessions, nodeID)
		return true
	}
	return false
}
