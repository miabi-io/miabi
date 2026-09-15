// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package controlmanager

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

const (
	kindContainer = "container"
	kindService   = "service"
)

type presence int

const (
	unknown presence = iota
	present
	missing
)

type observation struct {
	presence presence
	kind     string
}

// Skip is a node or cluster a sweep could not observe. Its apps are unknown, never missing: an offline node is
// not an empty one.
type Skip struct {
	Scope  string `json:"scope"` // node | cluster
	ID     uint   `json:"id"`
	Reason string `json:"reason"`
}

// observe judges container apps per node and service apps per cluster, since a service's tasks run wherever
// Swarm placed them and only its cluster's manager knows whether it exists.
func (s *Service) observe(ctx context.Context, apps []models.Application) (map[uint]observation, []Skip) {
	byNode := map[uint][]*models.Application{}
	byCluster := map[uint][]*models.Application{}
	for i := range apps {
		a := &apps[i]
		if a.RuntimeKind == models.RuntimeService {
			byCluster[a.ClusterID] = append(byCluster[a.ClusterID], a)
		} else {
			byNode[a.ServerID] = append(byNode[a.ServerID], a)
		}
	}
	seen := make(map[uint]observation, len(apps))
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

// observeNode lists a node's containers once and looks for each app's active release container. An exited
// container still counts as present: a crash belongs to the restart policy and the user, not to drift.
func (s *Service) observeNode(ctx context.Context, nodeID uint, apps []*models.Application, seen map[uint]observation) string {
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
	containers, err := dc.ListContainers(ctx, true)
	if err != nil {
		return "listing containers failed: " + err.Error()
	}
	for _, a := range apps {
		rel, err := s.releases.FindActive(a.ID)
		if err != nil || rel.ContainerID == "" {
			continue
		}
		o := observation{kind: kindContainer, presence: missing}
		for _, c := range containers {
			if strings.HasPrefix(c.ID, rel.ContainerID) {
				o.presence = present
				break
			}
		}
		seen[a.ID] = o
	}
	return ""
}

// observeCluster asks a cluster's manager for each service app's service. Only a not-found answer is missing;
// a manager that can't be reached leaves the whole cluster unknown.
func (s *Service) observeCluster(ctx context.Context, clusterID uint, apps []*models.Application, seen map[uint]observation) string {
	mgr, err := s.clusters.Manager(ctx, clusterID)
	if err != nil {
		return "manager unreachable: " + err.Error()
	}
	for _, a := range apps {
		_, err := mgr.ServiceInspect(ctx, node.AppAlias(a))
		switch {
		case err == nil:
			seen[a.ID] = observation{kind: kindService, presence: present}
		case errors.Is(err, docker.ErrNotFound):
			seen[a.ID] = observation{kind: kindService, presence: missing}
		}
	}
	return ""
}
