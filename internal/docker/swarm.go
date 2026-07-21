// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
)

// defaultSwarmListenAddr is the management-plane bind used when a caller does
// not specify one (the standard Docker Swarm port on all interfaces).
const defaultSwarmListenAddr = "0.0.0.0:2377"

// SwarmInfo summarizes an engine's participation in a Docker Swarm, as reported
// by `docker info`. It is available on any engine (no manager role required), so
// it is the cheap signal Miabi uses to auto-detect cluster mode.
type SwarmInfo struct {
	// LocalNodeState is the engine's swarm state: inactive | pending | active |
	// error | locked. "inactive" means plain (non-swarm) Docker.
	LocalNodeState string `json:"local_node_state"`
	// ControlAvailable is true when this engine is a reachable swarm manager
	// (i.e. it can drive cluster operations).
	ControlAvailable bool `json:"control_available"`
	// NodeID / NodeAddr identify this engine within the swarm; NodeAddr is the
	// address it advertises to peers (used as the join remote-address).
	NodeID   string `json:"node_id"`
	NodeAddr string `json:"node_addr"`
	// Managers / Nodes are cluster-wide counts (manager-reported; 0 elsewhere).
	Managers int `json:"managers"`
	Nodes    int `json:"nodes"`
	// RemoteManagers are the advertised addresses of the swarm's managers.
	RemoteManagers []string `json:"remote_managers,omitempty"`
	// Error carries the engine's swarm error string when LocalNodeState == error.
	Error string `json:"error,omitempty"`
}

// SwarmNode is one node as seen from a swarm manager (`docker node ls`).
type SwarmNode struct {
	ID            string `json:"id"`
	Hostname      string `json:"hostname"`
	Role          string `json:"role"`         // manager | worker
	Availability  string `json:"availability"` // active | pause | drain
	State         string `json:"state"`        // ready | down | unknown | disconnected
	Leader        bool   `json:"leader"`
	Reachability  string `json:"reachability,omitempty"` // reachable | unreachable (managers)
	Addr          string `json:"addr,omitempty"`
	EngineVersion string `json:"engine_version,omitempty"`
	// Capacity as the swarm scheduler sees it — what it packs tasks against. It comes
	// from the node's own report over the swarm control plane, so it is known even for
	// a node Miabi has no Docker client for (an unmanaged member with no agent), where
	// host metrics are otherwise unavailable.
	NanoCPUs    int64  `json:"nano_cpus,omitempty"`    // 1e9 == one core
	MemoryBytes int64  `json:"memory_bytes,omitempty"` // total, not used
	OS          string `json:"os,omitempty"`           // linux | windows
	Arch        string `json:"arch,omitempty"`         // x86_64 | aarch64 | …
	// Tasks is how many service tasks the scheduler currently runs here — the node's
	// load. Populated by SwarmNodes; 0 for an idle node.
	Tasks int `json:"tasks"`
}

// SwarmTask is one task (a container the scheduler placed) of a service, as seen
// from a manager. It is how a node's real workload is enumerated: the container
// itself lives on the node, which Miabi may hold no Docker client for.
type SwarmTask struct {
	ID           string `json:"id"`
	ServiceName  string `json:"service_name"`
	NodeID       string `json:"node_id"`
	Image        string `json:"image,omitempty"`
	Slot         int    `json:"slot,omitempty"`
	State        string `json:"state"`         // running | preparing | failed | …
	DesiredState string `json:"desired_state"` // running | shutdown
	Message      string `json:"message,omitempty"`
	Err          string `json:"error,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"` // RFC3339
}

// SwarmJoinTokens are the secrets a node uses to join a swarm in either role.
type SwarmJoinTokens struct {
	Worker  string `json:"worker"`
	Manager string `json:"manager"`
}

// SwarmInitRequest configures putting an engine into swarm mode as a manager.
type SwarmInitRequest struct {
	// AdvertiseAddr is the address managers/workers reach this manager on
	// (host or host:port). Required.
	AdvertiseAddr string
	// ListenAddr is the management-plane bind; blank defaults to 0.0.0.0:2377.
	ListenAddr string
	// DataPathAddr is the address for overlay (VXLAN) data-plane traffic; blank
	// falls back to AdvertiseAddr.
	DataPathAddr string
}

// SwarmJoinRequest configures joining an engine to an existing swarm.
type SwarmJoinRequest struct {
	// RemoteAddrs are the manager addresses to dial (host:port).
	RemoteAddrs []string
	// JoinToken is the worker or manager join token.
	JoinToken string
	// AdvertiseAddr is the address this node advertises to peers; blank lets the
	// engine auto-detect.
	AdvertiseAddr string
	// ListenAddr is the management-plane bind; blank defaults to 0.0.0.0:2377.
	ListenAddr string
}

// Swarm reports this engine's swarm participation, derived from `docker info`.
// It never requires a manager role, so it is safe to call on any node.
func (e *engineClient) Swarm(ctx context.Context) (SwarmInfo, error) {
	info, err := e.cli.Info(ctx)
	if err != nil {
		return SwarmInfo{}, err
	}
	s := info.Swarm
	out := SwarmInfo{
		LocalNodeState:   string(s.LocalNodeState),
		ControlAvailable: s.ControlAvailable,
		NodeID:           s.NodeID,
		NodeAddr:         s.NodeAddr,
		Managers:         s.Managers,
		Nodes:            s.Nodes,
		Error:            s.Error,
	}
	for _, rm := range s.RemoteManagers {
		if rm.Addr != "" {
			out.RemoteManagers = append(out.RemoteManagers, rm.Addr)
		}
	}
	return out, nil
}

// SwarmInit puts this engine into swarm mode as the first manager and returns
// its swarm node ID.
func (e *engineClient) SwarmInit(ctx context.Context, req SwarmInitRequest) (string, error) {
	listen := req.ListenAddr
	if listen == "" {
		listen = defaultSwarmListenAddr
	}
	return e.cli.SwarmInit(ctx, swarm.InitRequest{
		AdvertiseAddr: req.AdvertiseAddr,
		ListenAddr:    listen,
		DataPathAddr:  req.DataPathAddr,
	})
}

// SwarmJoin joins this engine to an existing swarm using the given token and
// manager remote address(es).
func (e *engineClient) SwarmJoin(ctx context.Context, req SwarmJoinRequest) error {
	listen := req.ListenAddr
	if listen == "" {
		listen = defaultSwarmListenAddr
	}
	return e.cli.SwarmJoin(ctx, swarm.JoinRequest{
		RemoteAddrs:   req.RemoteAddrs,
		JoinToken:     req.JoinToken,
		AdvertiseAddr: req.AdvertiseAddr,
		ListenAddr:    listen,
	})
}

// SwarmLeave removes this engine from its swarm. Leaving as the last manager
// requires force.
func (e *engineClient) SwarmLeave(ctx context.Context, force bool) error {
	return e.cli.SwarmLeave(ctx, force)
}

// SwarmJoinTokens returns the swarm's worker and manager join tokens. Requires
// this engine to be a reachable manager.
func (e *engineClient) SwarmJoinTokens(ctx context.Context) (SwarmJoinTokens, error) {
	sw, err := e.cli.SwarmInspect(ctx)
	if err != nil {
		return SwarmJoinTokens{}, err
	}
	return SwarmJoinTokens{Worker: sw.JoinTokens.Worker, Manager: sw.JoinTokens.Manager}, nil
}

// SwarmNodes lists the swarm's nodes. Requires this engine to be a reachable
// manager.
func (e *engineClient) SwarmNodes(ctx context.Context) ([]SwarmNode, error) {
	list, err := e.cli.NodeList(ctx, types.NodeListOptions{})
	if err != nil {
		return nil, err
	}
	// Task load per node, in one call rather than one per node. Best-effort: a
	// failure here leaves Tasks at 0 rather than failing the whole listing.
	load := e.swarmTaskCounts(ctx)

	out := make([]SwarmNode, 0, len(list))
	for _, n := range list {
		sn := SwarmNode{
			ID:            n.ID,
			Hostname:      n.Description.Hostname,
			Role:          string(n.Spec.Role),
			Availability:  string(n.Spec.Availability),
			State:         string(n.Status.State),
			Addr:          n.Status.Addr,
			EngineVersion: n.Description.Engine.EngineVersion,
			Tasks:         load[n.ID],
		}
		if r := n.Description.Resources; r.NanoCPUs > 0 || r.MemoryBytes > 0 {
			sn.NanoCPUs = r.NanoCPUs
			sn.MemoryBytes = r.MemoryBytes
		}
		sn.OS = n.Description.Platform.OS
		sn.Arch = n.Description.Platform.Architecture
		if n.ManagerStatus != nil {
			sn.Leader = n.ManagerStatus.Leader
			sn.Reachability = string(n.ManagerStatus.Reachability)
			if sn.Role == "" {
				sn.Role = string(swarm.NodeRoleManager)
			}
		}
		out = append(out, sn)
	}
	return out, nil
}

// swarmTaskCounts maps a swarm node id to the number of tasks running on it.
// Best-effort: an error yields an empty map, so callers simply report no load.
func (e *engineClient) swarmTaskCounts(ctx context.Context) map[string]int {
	tasks, err := e.cli.TaskList(ctx, types.TaskListOptions{
		Filters: filters.NewArgs(filters.Arg("desired-state", "running")),
	})
	if err != nil {
		return map[string]int{}
	}
	counts := make(map[string]int, len(tasks))
	for _, t := range tasks {
		if t.NodeID != "" && t.Status.State == swarm.TaskStateRunning {
			counts[t.NodeID]++
		}
	}
	return counts
}

// SwarmNodeRemove removes a node from the swarm's node list on the manager
// (after the node has left). force is needed for nodes that have not gracefully
// left. Used to keep the Nodes page free of stale "down" entries.
func (e *engineClient) SwarmNodeRemove(ctx context.Context, nodeID string, force bool) error {
	return e.cli.NodeRemove(ctx, nodeID, types.NodeRemoveOptions{Force: force})
}

// SwarmNodeAvailability sets a node's scheduling availability. Requires a manager.
//
//	active — the scheduler may place new tasks here
//	pause  — existing tasks keep running; no new ones are placed
//	drain  — existing tasks are rescheduled off this node, and none are placed
//
// Drain is what makes a node safe to reboot: without it Swarm keeps scheduling onto
// a host that is about to disappear.
//
// The update is version-checked (Docker's optimistic concurrency), so it is
// re-inspected immediately before writing rather than trusting a cached version.
func (e *engineClient) SwarmNodeAvailability(ctx context.Context, nodeID, availability string) error {
	var av swarm.NodeAvailability
	switch availability {
	case string(swarm.NodeAvailabilityActive):
		av = swarm.NodeAvailabilityActive
	case string(swarm.NodeAvailabilityPause):
		av = swarm.NodeAvailabilityPause
	case string(swarm.NodeAvailabilityDrain):
		av = swarm.NodeAvailabilityDrain
	default:
		return fmt.Errorf("unsupported availability %q (want active, pause or drain)", availability)
	}
	n, _, err := e.cli.NodeInspectWithRaw(ctx, nodeID)
	if err != nil {
		return wrapNotFound(err)
	}
	spec := n.Spec
	spec.Availability = av
	return e.cli.NodeUpdate(ctx, nodeID, n.Version, spec)
}

// SwarmTasks lists the swarm's tasks, optionally filtered to one node. Requires a
// manager. Only the manager can enumerate these: the containers live on the nodes,
// which Miabi may hold no Docker client for.
func (e *engineClient) SwarmTasks(ctx context.Context, nodeID string) ([]SwarmTask, error) {
	args := filters.NewArgs()
	if nodeID != "" {
		args.Add("node", nodeID)
	}
	list, err := e.cli.TaskList(ctx, types.TaskListOptions{Filters: args})
	if err != nil {
		return nil, err
	}
	// Task -> service name, resolved once rather than per task.
	svcs, serr := e.cli.ServiceList(ctx, types.ServiceListOptions{})
	names := map[string]string{}
	if serr == nil {
		for _, s := range svcs {
			names[s.ID] = s.Spec.Name
		}
	}
	out := make([]SwarmTask, 0, len(list))
	for _, t := range list {
		st := SwarmTask{
			ID:           t.ID,
			ServiceName:  names[t.ServiceID],
			NodeID:       t.NodeID,
			Slot:         t.Slot,
			State:        string(t.Status.State),
			DesiredState: string(t.DesiredState),
			Message:      t.Status.Message,
			Err:          t.Status.Err,
		}
		if cs := t.Spec.ContainerSpec; cs != nil {
			st.Image = cs.Image
		}
		if !t.Status.Timestamp.IsZero() {
			st.UpdatedAt = t.Status.Timestamp.UTC().Format(time.RFC3339)
		}
		out = append(out, st)
	}
	return out, nil
}
