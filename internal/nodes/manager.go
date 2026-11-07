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

package nodes

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/wstunnel"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

type Manager struct {
	clients *Clients
	nodes   *node.Service

	mu        sync.Mutex
	sessions  map[uint]*yamux.Session
	onConnect func(ctx context.Context, srv *models.Server, token string, dc docker.Client)
	onRemove  func(ctx context.Context, srv *models.Server, dc docker.Client)
	subscribe func(ctx context.Context, nodeID uint, dc docker.Client)
	// A fan-out because several things watch transitions (alerting, the console stream).
	statusHooks []func(nodeID uint, name string, online bool)
	// Lets a poller re-confirming a node every tick avoid announcing it every tick.
	online map[uint]bool
}

func NewManager(clients *Clients, nodes *node.Service) *Manager {
	return &Manager{clients: clients, nodes: nodes, sessions: map[uint]*yamux.Session{}, online: map[uint]bool{}}
}

// The hook gets the plaintext join token, which is only available at connect time.
func (m *Manager) SetOnConnect(fn func(ctx context.Context, srv *models.Server, token string, dc docker.Client)) {
	m.onConnect = fn
}

// The context is cancelled when the tunnel drops.
func (m *Manager) SetSubscriber(fn func(ctx context.Context, nodeID uint, dc docker.Client)) {
	m.subscribe = fn
}

// Runs with the live Docker client before the tunnel is closed. See Teardown.
func (m *Manager) SetOnRemove(fn func(ctx context.Context, srv *models.Server, dc docker.Client)) {
	m.onRemove = fn
}

// Listeners run in a goroutine, must not block, and see transitions only.
func (m *Manager) AddStatusListener(fn func(nodeID uint, name string, online bool)) {
	m.mu.Lock()
	m.statusHooks = append(m.statusHooks, fn)
	m.mu.Unlock()
}

// The status write happens every time (it carries the heartbeat); only the notification is transition-gated.
func (m *Manager) announce(nodeID uint, name string, online bool, agentVersion string) {
	if online {
		m.nodes.MarkConnected(nodeID, agentVersion)
	} else {
		m.nodes.MarkDisconnected(nodeID)
	}
	m.notify(nodeID, name, online)
}

func (m *Manager) notify(nodeID uint, name string, online bool) {
	m.mu.Lock()
	was, known := m.online[nodeID]
	m.online[nodeID] = online
	changed := !known || was != online
	hooks := make([]func(uint, string, bool), len(m.statusHooks))
	copy(hooks, m.statusHooks)
	m.mu.Unlock()
	if !changed {
		return
	}
	for _, fn := range hooks {
		go fn(nodeID, name, online)
	}
}

// Called on record deletion, not tunnel drop: a deleted node's id may later be reused by another machine.
func (m *Manager) ForgetStatus(nodeID uint) {
	m.mu.Lock()
	delete(m.online, nodeID)
	m.mu.Unlock()
}

// Call before Disconnect/DeleteNode so node-side infrastructure is cleaned up over the still-open tunnel.
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

// The plaintext join token is passed through so the connect hook can configure node-side infrastructure.
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
	logger.Info("node agent connected", "node", nodeID, "name", srv.Name, "agent", agentVersion)
	m.announce(nodeID, srv.Name, true, agentVersion)

	// Cancelled when the tunnel drops so per-node workers stop with their node.
	connCtx, cancelConn := context.WithCancel(context.Background())

	if m.onConnect != nil {
		go m.onConnect(connCtx, srv, token, dcli)
	}
	if m.subscribe != nil {
		go m.subscribe(connCtx, nodeID, dcli)
	}

	stop := make(chan struct{})
	go m.heartbeat(nodeID, agentVersion, stop)

	<-sess.CloseChan() // blocks until the tunnel dies (keepalive timeout or close)
	cancelConn()
	close(stop)

	// On a fast reconnect a superseded goroutine would otherwise remove the live client and flip the node offline.
	if m.forget(nodeID, sess) {
		m.clients.RemoveRemote(nodeID)
		logger.Info("node agent disconnected", "node", nodeID, "name", srv.Name)
		m.announce(nodeID, srv.Name, false, "")
		return
	}
	logger.Info("superseded node tunnel closed; keeping the live connection", "node", nodeID, "name", srv.Name)
}

// Connected reports whether a node currently has a live agent tunnel.
func (m *Manager) Connected(nodeID uint) bool { return m.clients.Connected(nodeID) }

// Generous so a momentarily slow node is not torn down; a healthy daemon answers in milliseconds.
const nodeHealthProbeTimeout = 8 * time.Second

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

// Catches half-open tunnels yamux keepalive missed by closing the session, which runs the normal disconnect cleanup.
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

// A node that dropped ungracefully self-heals to offline within ~30s rather than lingering online.
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

func (m *Manager) replace(nodeID uint, sess *yamux.Session) {
	m.mu.Lock()
	old := m.sessions[nodeID]
	m.sessions[nodeID] = sess
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// A superseded connection returns false so its goroutine skips the shared disconnect cleanup.
func (m *Manager) forget(nodeID uint, sess *yamux.Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[nodeID] == sess {
		delete(m.sessions, nodeID)
		return true
	}
	return false
}
