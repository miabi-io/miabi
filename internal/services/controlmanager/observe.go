// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package controlmanager

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/drift"
)

const (
	kindContainer = "container"
	kindService   = "service"
	kindVolume    = "volume"
	kindGateway   = "gateway"
)

type state int

const (
	// unknown is a sweep that could not see the item: an offline node, a fresh agent, an unreachable
	// manager. It is never evidence of absence.
	unknown state = iota
	intact
	gone
	// replaced is a volume that exists under its own name but was created again since Miabi recorded it:
	// the row survived, the data did not.
	replaced
)

type observation struct {
	state state
	kind  string
}

// Skip is a node or cluster a sweep could not observe. Its items are unknown, never missing: an offline
// node is not an empty one.
type Skip struct {
	Scope  string `json:"scope"` // node | cluster
	ID     uint   `json:"id"`
	Reason string `json:"reason"`
}

// observe judges container and volume items per node and service items per cluster, since a service's
// tasks run wherever Swarm placed them and only its cluster's manager knows whether it exists.
func (s *Service) observe(ctx context.Context, items []item) (map[string]observation, []Skip) {
	byNode := map[uint][]*item{}
	byCluster := map[uint][]*item{}
	for i := range items {
		it := &items[i]
		if it.kind == kindService {
			byCluster[it.clusterID] = append(byCluster[it.clusterID], it)
		} else {
			byNode[it.nodeID] = append(byNode[it.nodeID], it)
		}
	}
	seen := make(map[string]observation, len(items))
	var skipped []Skip
	for id, group := range byNode {
		if reason := s.observeNode(ctx, id, group, seen); reason != "" {
			skipped = append(skipped, Skip{Scope: "node", ID: id, Reason: reason})
		}
	}
	for id, group := range byCluster {
		if reason := s.observeCluster(ctx, id, group, seen); reason != "" {
			skipped = append(skipped, Skip{Scope: "cluster", ID: id, Reason: reason})
		}
	}
	sort.Slice(skipped, func(i, j int) bool {
		if skipped[i].Scope != skipped[j].Scope {
			return skipped[i].Scope > skipped[j].Scope
		}
		return skipped[i].ID < skipped[j].ID
	})
	return seen, skipped
}

// observeNode lists a node's containers and volumes once each, then judges every item placed on it. An
// exited container still counts as intact: a crash belongs to the restart policy and the user, not to drift.
func (s *Service) observeNode(ctx context.Context, nodeID uint, items []*item, seen map[string]observation) string {
	since, connected := s.nodes.ConnectedSince(nodeID)
	if !connected {
		return "offline"
	}
	if !since.IsZero() && s.now().Sub(since) < nodeGrace {
		return "agent connected recently"
	}
	dc, err := s.nodes.For(nodeID)
	if err != nil {
		return "offline"
	}

	var containers []docker.Container
	live := map[string]docker.Volume{}
	for _, it := range items {
		switch {
		case it.kind == kindContainer && containers == nil:
			containers, err = dc.ListContainers(ctx, true)
			if err != nil {
				return "listing containers failed: " + err.Error()
			}
		case it.kind == kindVolume && len(live) == 0:
			vols, verr := dc.ListVolumes(ctx)
			if verr != nil {
				return "listing volumes failed: " + verr.Error()
			}
			for _, v := range vols {
				live[v.Name] = v
			}
		}
	}

	for _, it := range items {
		switch it.kind {
		case kindContainer:
			seen[it.key] = observation{kind: kindContainer, state: containerState(containers, it.containerID)}
		case kindGateway:
			seen[it.key] = observation{kind: kindGateway, state: gatewayState(ctx, dc, it.gatewayName)}
		case kindVolume:
			st, adopt := volumeState(live, it.volumeName, it.engineCreatedAt)
			// A volume created before Miabi recorded a timestamp adopts the engine's, so it reads as intact
			// rather than replaced for the rest of its life.
			if adopt != "" && it.adopt != nil {
				_ = it.adopt(adopt)
			}
			seen[it.key] = observation{kind: kindVolume, state: st}
		}
	}
	return ""
}

// observeCluster asks a cluster's manager for each service app's service. Only a not-found answer is
// missing; a manager that can't be reached leaves the whole cluster unknown.
func (s *Service) observeCluster(ctx context.Context, clusterID uint, items []*item, seen map[string]observation) string {
	mgr, err := s.clusters.Manager(ctx, clusterID)
	if err != nil {
		return "manager unreachable: " + err.Error()
	}
	for _, it := range items {
		_, err := mgr.ServiceInspect(ctx, it.service)
		switch {
		case err == nil:
			seen[it.key] = observation{kind: kindService, state: intact}
		case errors.Is(err, docker.ErrNotFound):
			seen[it.key] = observation{kind: kindService, state: gone}
		}
	}
	return ""
}

// gatewayState asks the node about its gateway container by name. A gateway that exists but is not running
// counts as gone: containers run unless-stopped, so one that is down was stopped by hand or cannot start, and
// either way the node is serving nothing.
func gatewayState(ctx context.Context, dc docker.Client, name string) state {
	c, err := dc.InspectContainer(ctx, name)
	switch {
	case errors.Is(err, docker.ErrNotFound):
		return gone
	case err != nil:
		return unknown
	case c.State != "running":
		return gone
	default:
		return intact
	}
}

func containerState(containers []docker.Container, containerID string) state {
	for _, c := range containers {
		if strings.HasPrefix(c.ID, containerID) {
			return intact
		}
	}
	return gone
}

// volumeState judges a volume against the node's live volumes, and reports the engine timestamp to adopt
// when Miabi has none recorded. What counts as replaced is drift's rule, shared with the guard that
// refuses to start a workload whose data is gone.
func volumeState(live map[string]docker.Volume, name, recorded string) (state, string) {
	v, ok := live[name]
	switch {
	case !ok:
		return gone, ""
	case recorded == "":
		return intact, v.CreatedAt
	case drift.Replaced(recorded, v.CreatedAt):
		return replaced, ""
	default:
		return intact, ""
	}
}

// class names the drift a state represents, for the report and the events.
func (st state) class() string {
	if st == replaced {
		return drift.ClassReplaced
	}
	return drift.ClassMissing
}
