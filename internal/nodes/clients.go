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
	"errors"
	"sync"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/pkg/stack/selfcontainer"
)

var ErrNodeOffline = errors.New("node is offline (no connected agent)")

// SwarmManagers resolves the client that drives a cluster's swarm. Implemented by the cluster service.
type SwarmManagers func(ctx context.Context, clusterID uint) (docker.Client, error)

// Server id 0 resolves to the local node so paths without placement still work single-node.
type Clients struct {
	mu      sync.RWMutex
	localID uint
	local   docker.Client
	remote  map[uint]docker.Client
	// connectedAt is when each remote client was registered: a fresh agent's view can still be partial.
	connectedAt map[uint]time.Time

	// Miabi's own containers, protected from being killed via the admin containers list.
	localSelf  string
	remoteSelf map[uint]string

	// nil falls back to the local engine, which manages only the default cluster's swarm.
	managers SwarmManagers
}

// NewClients creates the registry seeded with the local node's client.
func NewClients(localID uint, local docker.Client) *Clients {
	return &Clients{localID: localID, local: local, remote: map[uint]docker.Client{}, connectedAt: map[uint]time.Time{}, remoteSelf: map[uint]string{}}
}

func (c *Clients) SetSwarmManagers(fn SwarmManagers) {
	c.mu.Lock()
	c.managers = fn
	c.mu.Unlock()
}

func (c *Clients) SetLocalSelf(id string) {
	c.mu.Lock()
	c.localSelf = id
	c.mu.Unlock()
}

func (c *Clients) SetRemoteSelf(serverID uint, id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	c.remoteSelf[serverID] = id
	c.mu.Unlock()
}

func (c *Clients) SelfContainerID(serverID uint) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.IsLocal(serverID) {
		return c.localSelf
	}
	return c.remoteSelf[serverID]
}

func (c *Clients) IsSelfContainer(serverID uint, containerID string) bool {
	c.mu.RLock()
	self := c.remoteSelf[serverID]
	if c.IsLocal(serverID) {
		self = c.localSelf
	}
	c.mu.RUnlock()
	return self != "" && selfcontainer.Match(self, containerID)
}

// LocalID returns the local node's server id.
func (c *Clients) LocalID() uint { return c.localID }

// Local returns the local node's Docker client.
func (c *Clients) Local() docker.Client { return c.local }

// IsLocal reports whether the server id refers to the local node.
func (c *Clients) IsLocal(serverID uint) bool {
	return serverID == 0 || serverID == c.localID
}

func (c *Clients) For(serverID uint) (docker.Client, error) {
	if c.IsLocal(serverID) {
		return c.local, nil
	}
	c.mu.RLock()
	cl, ok := c.remote[serverID]
	c.mu.RUnlock()
	if !ok {
		return nil, ErrNodeOffline
	}
	return cl, nil
}

// Distinct from ErrNotFound: reporting "nothing running" for a 1/1 service would be false.
var ErrTaskUnreachable = errors.New("the service's task runs on a swarm node with no Miabi agent")

// Asks each engine directly because correlating the task's swarm node id is unreliable.
func (c *Clients) ForServiceTask(ctx context.Context, clusterID uint, serviceName string) (docker.Client, string, error) {
	c.mu.RLock()
	engines := make([]docker.Client, 0, len(c.remote)+1)
	if c.local != nil {
		engines = append(engines, c.local) // local first: free, and the common case
	}
	for _, cl := range c.remote {
		engines = append(engines, cl)
	}
	managers := c.managers
	c.mu.RUnlock()

	for _, dc := range engines {
		if cid, err := dc.ServiceTaskContainerID(ctx, serviceName); err == nil && cid != "" {
			return dc, cid, nil
		}
	}
	// If the manager says it runs, the task is on a node we have no client for; "nothing running" would be a lie.
	mgr := c.local
	if managers != nil {
		mgr = nil
		if m, err := managers(ctx, clusterID); err == nil {
			mgr = m
		}
	}
	if mgr != nil {
		if st, err := mgr.ServiceInspect(ctx, serviceName); err == nil && st.RunningTasks > 0 {
			return nil, "", ErrTaskUnreachable
		}
	}
	return nil, "", docker.ErrNotFound
}

// Connected reports whether a node currently has a usable Docker client.
func (c *Clients) Connected(serverID uint) bool {
	if c.IsLocal(serverID) {
		return true
	}
	c.mu.RLock()
	_, ok := c.remote[serverID]
	c.mu.RUnlock()
	return ok
}

func (c *Clients) RemoteIDs() []uint {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]uint, 0, len(c.remote))
	for id := range c.remote {
		ids = append(ids, id)
	}
	return ids
}

func (c *Clients) SetRemote(serverID uint, cl docker.Client) {
	c.mu.Lock()
	c.remote[serverID] = cl
	c.connectedAt[serverID] = time.Now()
	c.mu.Unlock()
}

// The local engine is always reachable, so it reports the zero time.
func (c *Clients) ConnectedSince(serverID uint) (time.Time, bool) {
	if c.IsLocal(serverID) {
		return time.Time{}, true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.remote[serverID]; !ok {
		return time.Time{}, false
	}
	return c.connectedAt[serverID], true
}

// RemoveRemote drops a remote node's client when its agent disconnects.
func (c *Clients) RemoveRemote(serverID uint) {
	c.mu.Lock()
	if cl, ok := c.remote[serverID]; ok {
		_ = cl.Close()
		delete(c.remote, serverID)
	}
	delete(c.connectedAt, serverID)
	delete(c.remoteSelf, serverID)
	c.mu.Unlock()
}
