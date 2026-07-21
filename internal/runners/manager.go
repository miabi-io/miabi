// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package runners maintains the live tunnels of connected build/pipeline
// runners. It mirrors the node-agent connection manager (internal/nodes) but is
// deliberately thinner: a runner uses its OWN local Docker/BuildKit daemon, so
// the control plane never dials into it — the tunnel exists only to lease jobs
// to the runner and stream logs/status back (job leasing lands in P3). Here it
// carries registration + heartbeat + online/offline, and holds the multiplexed
// session so the later lease dispatcher can open streams to the runner.
package runners

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/wstunnel"
)

// heartbeatInterval refreshes a connected runner's last-seen, matching the node
// agent's cadence.
const heartbeatInterval = 30 * time.Second

// connectionState is what the runner service needs to reflect a live tunnel.
// Implemented by runner.Service (MarkConnected/MarkDisconnected); an interface
// keeps this package free of a service import cycle.
type connectionState interface {
	MarkConnected(id uint, os, arch, version, remoteIP string)
	MarkDisconnected(id uint)
}

// Manager accepts runner tunnels and tracks each runner's live session and
// online/offline status.
type Manager struct {
	state connectionState

	mu       sync.Mutex
	sessions map[uint]*yamux.Session
	onStatus func(r *models.Runner, online bool)
}

// NewManager creates a runner connection manager backed by the runner service
// (for status persistence).
func NewManager(state connectionState) *Manager {
	return &Manager{state: state, sessions: map[uint]*yamux.Session{}}
}

// SetOnStatusChange registers a hook fired when a runner comes online (tunnel
// connected) or goes offline (tunnel dropped). Used by alerting to raise/resolve
// a "runner offline" alert. Best-effort: run in a goroutine, must not block.
func (m *Manager) SetOnStatusChange(fn func(r *models.Runner, online bool)) {
	m.onStatus = fn
}

// Handle owns an authenticated runner WebSocket: it builds the tunnel, marks the
// runner online, and blocks until the tunnel closes (so the HTTP handler keeps
// the connection open). The caller has already validated the registration token.
// os/arch/version are the runner's self-reported platform facts.
func (m *Manager) Handle(r *models.Runner, os, arch, version, remoteIP string, ws *websocket.Conn) {
	id := r.ID
	// Control plane OPENS streams to the runner (to dispatch job leases), so the
	// runner side ACCEPTS them — mirrors the node/agent role split.
	sess, err := wstunnel.Client(ws)
	if err != nil {
		logger.Error("failed to start runner tunnel", "runner", id, "error", err)
		_ = ws.Close()
		return
	}

	m.replace(id, sess)
	m.state.MarkConnected(id, os, arch, version, remoteIP)
	logger.Info("runner connected", "runner", id, "name", r.Name, "version", version, "ip", remoteIP)
	if m.onStatus != nil {
		go m.onStatus(r, true)
	}

	stop := make(chan struct{})
	go m.heartbeat(id, os, arch, version, remoteIP, stop)

	<-sess.CloseChan() // blocks until the tunnel dies (keepalive timeout or close)
	close(stop)

	m.state.MarkDisconnected(id)
	m.forget(id, sess)
	logger.Info("runner disconnected", "runner", id, "name", r.Name)
	if m.onStatus != nil {
		go m.onStatus(r, false)
	}
}

// Connected reports whether a runner currently has a live tunnel.
func (m *Manager) Connected(id uint) bool {
	m.mu.Lock()
	_, ok := m.sessions[id]
	m.mu.Unlock()
	return ok
}

// Session returns a connected runner's multiplexed session (for the P3 lease
// dispatcher to open streams). ok is false when the runner is offline.
func (m *Manager) Session(id uint) (*yamux.Session, bool) {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	m.mu.Unlock()
	return sess, ok
}

// Disconnect tears down a runner's tunnel (e.g. when it is deleted or disabled).
func (m *Manager) Disconnect(id uint) {
	m.mu.Lock()
	sess := m.sessions[id]
	m.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

func (m *Manager) heartbeat(id uint, os, arch, version, remoteIP string, stop <-chan struct{}) {
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			m.state.MarkConnected(id, os, arch, version, remoteIP)
		}
	}
}

// replace registers the new session, closing any prior one (a reconnect
// supersedes a stale tunnel).
func (m *Manager) replace(id uint, sess *yamux.Session) {
	m.mu.Lock()
	old := m.sessions[id]
	m.sessions[id] = sess
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

func (m *Manager) forget(id uint, sess *yamux.Session) {
	m.mu.Lock()
	if m.sessions[id] == sess {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
}
