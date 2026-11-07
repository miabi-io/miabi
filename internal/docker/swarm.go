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

package docker

import (
	"context"
	"fmt"
	"time"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

const defaultSwarmListenAddr = "0.0.0.0:2377"

// SwarmInfo needs no manager role, so it is the cheap signal for auto-detecting cluster mode.
type SwarmInfo struct {
	// "inactive" means plain (non-swarm) Docker.
	LocalNodeState   string `json:"local_node_state"`
	ControlAvailable bool   `json:"control_available"`
	// NodeAddr is advertised to peers and used as the join remote-address.
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
	// Reported over the swarm control plane, so known even for nodes Miabi has no Docker client for.
	NanoCPUs    int64  `json:"nano_cpus,omitempty"`    // 1e9 == one core
	MemoryBytes int64  `json:"memory_bytes,omitempty"` // total, not used
	OS          string `json:"os,omitempty"`           // linux | windows
	Arch        string `json:"arch,omitempty"`         // x86_64 | aarch64 | …
	Tasks       int    `json:"tasks"`
}

// Only a manager can enumerate tasks; the containers live on nodes Miabi may hold no client for.
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
	AdvertiseAddr string
	// ListenAddr is the management-plane bind; blank defaults to 0.0.0.0:2377.
	ListenAddr   string
	DataPathAddr string
}

// SwarmJoinRequest configures joining an engine to an existing swarm.
type SwarmJoinRequest struct {
	// RemoteAddrs are the manager addresses to dial (host:port).
	RemoteAddrs []string
	// JoinToken is the worker or manager join token.
	JoinToken     string
	AdvertiseAddr string
	// ListenAddr is the management-plane bind; blank defaults to 0.0.0.0:2377.
	ListenAddr string
}

func (e *engineClient) Swarm(ctx context.Context) (SwarmInfo, error) {
	res, err := e.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return SwarmInfo{}, err
	}
	s := res.Info.Swarm
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

func (e *engineClient) SwarmInit(ctx context.Context, req SwarmInitRequest) (string, error) {
	listen := req.ListenAddr
	if listen == "" {
		listen = defaultSwarmListenAddr
	}
	res, err := e.cli.SwarmInit(ctx, client.SwarmInitOptions{
		AdvertiseAddr: req.AdvertiseAddr,
		ListenAddr:    listen,
		DataPathAddr:  req.DataPathAddr,
	})
	if err != nil {
		return "", err
	}
	return res.NodeID, nil
}

func (e *engineClient) SwarmJoin(ctx context.Context, req SwarmJoinRequest) error {
	listen := req.ListenAddr
	if listen == "" {
		listen = defaultSwarmListenAddr
	}
	_, err := e.cli.SwarmJoin(ctx, client.SwarmJoinOptions{
		RemoteAddrs:   req.RemoteAddrs,
		JoinToken:     req.JoinToken,
		AdvertiseAddr: req.AdvertiseAddr,
		ListenAddr:    listen,
	})
	return err
}

// Leaving as the last manager requires force.
func (e *engineClient) SwarmLeave(ctx context.Context, force bool) error {
	_, err := e.cli.SwarmLeave(ctx, client.SwarmLeaveOptions{Force: force})
	return err
}

func (e *engineClient) SwarmJoinTokens(ctx context.Context) (SwarmJoinTokens, error) {
	res, err := e.cli.SwarmInspect(ctx, client.SwarmInspectOptions{})
	if err != nil {
		return SwarmJoinTokens{}, err
	}
	return SwarmJoinTokens{Worker: res.Swarm.JoinTokens.Worker, Manager: res.Swarm.JoinTokens.Manager}, nil
}

func (e *engineClient) SwarmNodes(ctx context.Context) ([]SwarmNode, error) {
	res, err := e.cli.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return nil, err
	}
	// Best-effort: a failure leaves Tasks at 0 rather than failing the listing.
	load := e.swarmTaskCounts(ctx)

	out := make([]SwarmNode, 0, len(res.Items))
	for _, n := range res.Items {
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

func (e *engineClient) swarmTaskCounts(ctx context.Context) map[string]int {
	tasks, err := e.cli.TaskList(ctx, client.TaskListOptions{
		Filters: selectorFilters("desired-state", "running"),
	})
	if err != nil {
		return map[string]int{}
	}
	counts := make(map[string]int, len(tasks.Items))
	for _, t := range tasks.Items {
		if t.NodeID != "" && t.Status.State == swarm.TaskStateRunning {
			counts[t.NodeID]++
		}
	}
	return counts
}

// force is needed for nodes that did not leave gracefully.
func (e *engineClient) SwarmNodeRemove(ctx context.Context, nodeID string, force bool) error {
	_, err := e.cli.NodeRemove(ctx, nodeID, client.NodeRemoveOptions{Force: force})
	return err
}

// Version-checked, so the node is re-inspected immediately before writing.
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
	res, err := e.cli.NodeInspect(ctx, nodeID, client.NodeInspectOptions{})
	if err != nil {
		return wrapNotFound(err)
	}
	spec := res.Node.Spec
	spec.Availability = av
	_, err = e.cli.NodeUpdate(ctx, nodeID, client.NodeUpdateOptions{Version: res.Node.Version, Spec: spec})
	return err
}

func (e *engineClient) SwarmNodeSetLabel(ctx context.Context, nodeID, key, value string) error {
	res, err := e.cli.NodeInspect(ctx, nodeID, client.NodeInspectOptions{})
	if err != nil {
		return wrapNotFound(err)
	}
	spec := res.Node.Spec
	if v, ok := spec.Labels[key]; ok && v == value {
		return nil
	}
	if spec.Labels == nil {
		spec.Labels = map[string]string{}
	}
	spec.Labels[key] = value
	_, err = e.cli.NodeUpdate(ctx, nodeID, client.NodeUpdateOptions{Version: res.Node.Version, Spec: spec})
	return err
}

func (e *engineClient) SwarmTasks(ctx context.Context, nodeID string) ([]SwarmTask, error) {
	var filterPairs []string
	if nodeID != "" {
		filterPairs = append(filterPairs, "node", nodeID)
	}
	list, err := e.cli.TaskList(ctx, client.TaskListOptions{Filters: selectorFilters(filterPairs...)})
	if err != nil {
		return nil, err
	}
	// Task -> service name, resolved once rather than per task.
	svcs, serr := e.cli.ServiceList(ctx, client.ServiceListOptions{})
	names := map[string]string{}
	if serr == nil {
		for _, s := range svcs.Items {
			names[s.ID] = s.Spec.Name
		}
	}
	out := make([]SwarmTask, 0, len(list.Items))
	for _, t := range list.Items {
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
