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
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// Agent nodes are connected by the tunnel manager and return an error here.
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

func isDirect(srv *models.Server) bool {
	return srv.AccessMode == models.AccessSocket ||
		srv.AccessMode == models.AccessAPI
}

// Best-effort: the node stays registered as offline until the next attempt.
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

// A live agent tunnel is left untouched: rebuilding would evict the live client and flip the node offline.
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

// Blocks until ctx is cancelled.
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
				m.announce(srv.ID, srv.Name, false, "")
				continue
			}
		}
		dc, err := m.clients.For(srv.ID)
		if err != nil {
			m.announce(srv.ID, srv.Name, false, "")
			continue
		}
		pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = dc.Ping(pingCtx)
		cancel()
		m.announce(srv.ID, srv.Name, err == nil, "")
	}
}
