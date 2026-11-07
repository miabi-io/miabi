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
	// Never evidence of absence.
	unknown state = iota
	intact
	gone
	// The row survived, the data did not.
	replaced
)

type observation struct {
	state state
	kind  string
}

// An offline node is not an empty one.
type Skip struct {
	Scope  string `json:"scope"` // node | cluster
	ID     uint   `json:"id"`
	Reason string `json:"reason"`
}

// Services are judged per cluster since only the manager knows whether one exists.
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

// An exited container counts as intact: a crash belongs to the restart policy, not drift.
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
			// Adopting the engine's timestamp keeps a pre-recording volume from reading as replaced forever.
			if adopt != "" && it.adopt != nil {
				_ = it.adopt(adopt)
			}
			seen[it.key] = observation{kind: kindVolume, state: st}
		}
	}
	return ""
}

// Only not-found is missing; an unreachable manager leaves the whole cluster unknown.
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

// Containers run unless-stopped, so a gateway that isn't running counts as gone.
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

// The replaced rule is shared with the guard that refuses to start a workload whose data is gone.
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
