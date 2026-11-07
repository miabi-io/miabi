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

package proxy

import (
	"context"
	"sync"
)

type Memory struct {
	mu          sync.RWMutex
	routes      map[uint]RenderedRoute      // by route id, across workspaces
	middlewares map[uint]RenderedMiddleware // by middleware id, across workspaces
	registry    RegistryProxy               // last registry proxy config
}

func NewMemory() *Memory {
	return &Memory{
		routes:      make(map[uint]RenderedRoute),
		middlewares: make(map[uint]RenderedMiddleware),
	}
}

func (m *Memory) SyncWorkspace(_ context.Context, workspaceID uint, routes []RenderedRoute, mws []RenderedMiddleware) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.routes {
		if r.WorkspaceID == workspaceID {
			delete(m.routes, id)
		}
	}
	for id, mw := range m.middlewares {
		if mw.WorkspaceID == workspaceID {
			delete(m.middlewares, id)
		}
	}
	for _, r := range routes {
		m.routes[r.ID] = r
	}
	for _, mw := range mws {
		m.middlewares[mw.ID] = mw
	}
	return nil
}

func (m *Memory) SyncRegistry(_ context.Context, cfg RegistryProxy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registry = cfg
	return nil
}

// GetRoute returns the recorded route (used in tests).
func (m *Memory) GetRoute(id uint) (RenderedRoute, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.routes[id]
	return r, ok
}
