// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nodes manages the control plane's view of cluster nodes: the per-node
// Docker client registry and the agent connection manager.
package nodes

import (
	"context"
	"errors"
	"sync"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/pkg/stack/selfcontainer"
)

// ErrNodeOffline is returned when a Docker client is requested for a node whose
// agent is not currently connected.
var ErrNodeOffline = errors.New("node is offline (no connected agent)")

// Clients resolves a Docker client for a given server/node id: the local node uses the direct
// engine client, remote nodes a tunneled client registered when their agent connects. A server id
// of 0 resolves to the local node, so paths that don't carry placement still work single-node.
type Clients struct {
	mu      sync.RWMutex
	localID uint
	local   docker.Client
	remote  map[uint]docker.Client

	// Container IDs of Miabi's own runtime, used to stop these from being killed via the admin containers
	// list. localSelf is the control-plane (manager) container; remoteSelf holds each connected node's agent
	// container, as reported by the agent at connect time.
	localSelf  string
	remoteSelf map[uint]string
}

// NewClients creates the registry seeded with the local node's client.
func NewClients(localID uint, local docker.Client) *Clients {
	return &Clients{localID: localID, local: local, remote: map[uint]docker.Client{}, remoteSelf: map[uint]string{}}
}

// SetLocalSelf records the control-plane's own container ID (detected at
// startup) so it is protected from removal on the local node.
func (c *Clients) SetLocalSelf(id string) {
	c.mu.Lock()
	c.localSelf = id
	c.mu.Unlock()
}

// SetRemoteSelf records a connected node's agent container ID (reported by the
// agent) so it is protected from removal on that node.
func (c *Clients) SetRemoteSelf(serverID uint, id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	c.remoteSelf[serverID] = id
	c.mu.Unlock()
}

// SelfContainerID returns Miabi's own runtime container ID on the node — the
// control plane locally, or the node's agent — or "" when unknown (e.g. an
// offline agent or detection that found nothing).
func (c *Clients) SelfContainerID(serverID uint) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.IsLocal(serverID) {
		return c.localSelf
	}
	return c.remoteSelf[serverID]
}

// IsSelfContainer reports whether containerID is Miabi's own runtime
// container on the given node (the control plane locally, or that node's agent).
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

// For returns the Docker client for a node, or ErrNodeOffline if a remote node
// has no connected agent.
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

// ErrTaskUnreachable is returned when a swarm service HAS a running task but no engine we hold a
// client for can see its container — Swarm placed it on a node with no Miabi agent. It is
// distinct from docker.ErrNotFound: reporting "nothing running" for a 1/1 service is simply false.
var ErrTaskUnreachable = errors.New("the service's task runs on a swarm node with no Miabi agent")

// ForServiceTask resolves the engine and container id of a task of the named swarm service,
// wherever the scheduler placed it. Correlating the task's swarm node id is unreliable, so it asks
// the engines directly — local first, then each connected node. Exactly one node runs the task.
func (c *Clients) ForServiceTask(ctx context.Context, serviceName string) (docker.Client, string, error) {
	c.mu.RLock()
	engines := make([]docker.Client, 0, len(c.remote)+1)
	if c.local != nil {
		engines = append(engines, c.local) // local first: free, and the common case
	}
	for _, cl := range c.remote {
		engines = append(engines, cl)
	}
	c.mu.RUnlock()

	for _, dc := range engines {
		if cid, err := dc.ServiceTaskContainerID(ctx, serviceName); err == nil && cid != "" {
			return dc, cid, nil
		}
	}
	// No engine has it. Ask the manager whether the service is running anyway: if it
	// is, the task sits on a node we hold no client for, and saying "nothing is
	// running" would be a lie.
	if c.local != nil {
		if st, err := c.local.ServiceInspect(ctx, serviceName); err == nil && st.RunningTasks > 0 {
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

// RemoteIDs returns the ids of remote nodes that currently have a live client
// (the local node is excluded). Used by the health sweep to probe each tunnel.
func (c *Clients) RemoteIDs() []uint {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]uint, 0, len(c.remote))
	for id := range c.remote {
		ids = append(ids, id)
	}
	return ids
}

// SetRemote registers (or replaces) a remote node's client when its agent
// connects.
func (c *Clients) SetRemote(serverID uint, cl docker.Client) {
	c.mu.Lock()
	c.remote[serverID] = cl
	c.mu.Unlock()
}

// RemoveRemote drops a remote node's client when its agent disconnects.
func (c *Clients) RemoveRemote(serverID uint) {
	c.mu.Lock()
	if cl, ok := c.remote[serverID]; ok {
		_ = cl.Close()
		delete(c.remote, serverID)
	}
	delete(c.remoteSelf, serverID)
	c.mu.Unlock()
}
