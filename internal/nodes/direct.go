// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package nodes

import (
	"context"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// BuildDirectClient creates a Docker client for a non-agent remote node (socket
// or api) from its stored record. Agent nodes are connected by the tunnel
// manager instead and return an error here.
func BuildDirectClient(srv *models.Server) (docker.Client, error) {
	switch srv.AccessMode {
	case models.AccessSocket:
		return docker.NewSocket(srv.DockerEndpoint)
	case models.AccessAPI:
		var tlsm *docker.TLSMaterial
		if srv.TLSCACert != "" || srv.TLSCert != "" {
			key := ""
			if srv.TLSKeyEnc != "" {
				if dec, err := crypto.Decrypt(srv.TLSKeyEnc); err == nil {
					key = dec
				}
			}
			tlsm = &docker.TLSMaterial{CACert: []byte(srv.TLSCACert), Cert: []byte(srv.TLSCert), Key: []byte(key)}
		}
		return docker.NewTCP(srv.DockerEndpoint, tlsm)
	default:
		return nil, fmt.Errorf("node %d is not a direct-access node", srv.ID)
	}
}

// isDirect reports whether a node is reached by a control-plane-built client
// (socket/api) rather than an inbound agent tunnel.
func isDirect(srv *models.Server) bool {
	return srv.AccessMode == models.AccessSocket ||
		srv.AccessMode == models.AccessAPI
}

// ConnectDirect builds and registers a direct node's Docker client, replacing any existing one.
// A no-op for agent or local nodes. Best-effort: a build error is returned so callers can surface
// it, but the node stays registered as offline until the next attempt.
func (m *Manager) ConnectDirect(srv *models.Server) error {
	if srv == nil || srv.IsLocal || !isDirect(srv) {
		return nil
	}
	cl, err := BuildDirectClient(srv)
	if err != nil {
		return err
	}
	m.clients.SetRemote(srv.ID, cl)
	return nil
}

// DisconnectDirect drops a direct node's client (on delete or mode change).
func (m *Manager) DisconnectDirect(id uint) { m.clients.RemoveRemote(id) }

// RefreshClient rebuilds a node's Docker client after its connectivity settings change. A live
// agent tunnel owns its own registration and is left untouched — rebuilding would evict the live
// client and flip a connected node offline. Direct nodes are rebuilt to pick up new credentials.
func (m *Manager) RefreshClient(srv *models.Server) {
	if srv == nil || srv.IsLocal {
		return
	}
	m.mu.Lock()
	_, hasAgentSession := m.sessions[srv.ID]
	m.mu.Unlock()
	if hasAgentSession {
		return // the agent owns its client; don't disturb a live tunnel
	}
	m.clients.RemoveRemote(srv.ID) // clear any stale (e.g. previous-mode) client
	_ = m.ConnectDirect(srv)       // no-op for agent mode; builds for socket/api
}

// LoadDirect registers clients for all direct-access nodes at startup and runs a
// health poller that pings them on interval to keep their status current. It
// blocks until ctx is cancelled.
func (m *Manager) LoadDirect(ctx context.Context, interval time.Duration) {
	m.refreshDirect(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.refreshDirect(ctx)
		}
	}
}

// refreshDirect ensures every direct node has a client and pings each to update
// its online/offline status.
func (m *Manager) refreshDirect(ctx context.Context) {
	servers, err := m.nodes.List(ctx)
	if err != nil {
		return
	}
	for i := range servers {
		srv := &servers[i]
		if srv.IsLocal || !isDirect(srv) {
			continue
		}
		if !m.clients.Connected(srv.ID) {
			if err := m.ConnectDirect(srv); err != nil {
				logger.Warn("failed to connect node", "node", srv.ID, "mode", srv.AccessMode, "error", err)
				m.nodes.MarkDisconnected(srv.ID)
				continue
			}
		}
		dc, err := m.clients.For(srv.ID)
		if err != nil {
			m.nodes.MarkDisconnected(srv.ID)
			continue
		}
		pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = dc.Ping(pingCtx)
		cancel()
		if err != nil {
			m.nodes.MarkDisconnected(srv.ID)
		} else {
			m.nodes.MarkConnected(srv.ID, "")
		}
	}
}
