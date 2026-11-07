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

package apply

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
)

type NodeStatus string

const (
	NodeSynced    NodeStatus = "synced"      // live state matches desired (noop)
	NodeOutOfSync NodeStatus = "out_of_sync" // exists but drifted (update)
	NodeMissing   NodeStatus = "missing"     // declared but not yet created (create)
	NodeOrphaned  NodeStatus = "orphaned"    // live + GitOps-owned but absent from desired (delete)
)

type TopologyNode struct {
	Key    string     `json:"key"`  // "<Kind>/<name>" — stable node id, matches edge endpoints
	Kind   string     `json:"kind"` // Application, Database, Volume, Route, Stack, Secret, Domain
	Name   string     `json:"name"`
	Status NodeStatus `json:"status"`
	// LiveID is 0 when the resource is not yet created.
	LiveID uint   `json:"live_id,omitempty"`
	Slug   string `json:"slug,omitempty"`
	// Empty when the kind has no runtime state or is not live yet.
	Health string `json:"health,omitempty"`
	URL    string `json:"url,omitempty"`
}

type Topology struct {
	Nodes  []TopologyNode     `json:"nodes"`
	Edges  []declarative.Edge `json:"edges"`
	Counts map[NodeStatus]int `json:"counts"`
	// On a load error, nodes reflect last-known live state so the view degrades rather than going blank.
	Error string `json:"error,omitempty"`
	// Lets the UI label statuses as last-known rather than authoritative.
	Live bool `json:"live,omitempty"`
}

func statusFromAction(a declarative.Action) NodeStatus {
	switch a {
	case declarative.ActionUpdate:
		return NodeOutOfSync
	case declarative.ActionCreate:
		return NodeMissing
	case declarative.ActionDelete:
		return NodeOrphaned
	default:
		return NodeSynced
	}
}

// Orphaned GitOps-owned resources are included so the graph shows prune candidates.
func (s *Service) Topology(ctx context.Context, workspaceID uint, manifests []byte, opts Options) (*Topology, error) {
	// Parse for edges before render() rewrites env in place; broken manifests fall back to live state.
	desired, err := declarative.Parse(manifests)
	if err != nil {
		return s.LiveTopology(ctx, workspaceID, fmt.Sprintf("%s: %v", ErrInvalidManifest, err))
	}
	edges := declarative.Edges(desired)

	s.render(workspaceID, desired)
	actual, err := s.snapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	// Prune is restricted to this project so a sibling project's apps aren't shown as orphans.
	plan := declarative.BuildPlan(desired, actual, declarative.PlanOptions{
		Prune: true, PruneManagedBy: ManagedByGitOps, PruneGitOpsSource: opts.OwnerSource, IncludeNoop: true,
	})

	topo := &Topology{Edges: edges, Counts: map[NodeStatus]int{}}
	for _, ch := range plan.Changes {
		if ch.Kind == declarative.KindProject {
			continue // Project is a structural container, not a graph node
		}
		status := statusFromAction(ch.Action)
		node := TopologyNode{
			Key:    string(ch.Kind) + "/" + ch.Name,
			Kind:   string(ch.Kind),
			Name:   ch.Name,
			Status: status,
		}
		if status != NodeMissing {
			s.resolveLive(workspaceID, ch.Kind, ch.Name, &node)
		}
		topo.Nodes = append(topo.Nodes, node)
		topo.Counts[status]++
	}
	return topo, nil
}

// Cheap (no clone, no inspect) so the detail page can poll it between full loads.
func (s *Service) LiveStatus(workspaceID uint) map[string]string {
	out := map[string]string{}
	if apps, err := s.apps.List(workspaceID); err == nil {
		for i := range apps {
			out["Application/"+apps[i].Name] = string(apps[i].Status)
		}
	}
	if dbs, err := s.dbs.List(workspaceID); err == nil {
		for i := range dbs {
			out["Database/"+dbs[i].Name] = string(dbs[i].Status)
		}
	}
	return out
}

// Edges come from live specs, so env-template links do not survive.
func (s *Service) LiveTopology(ctx context.Context, workspaceID uint, syncError string) (*Topology, error) {
	actual, err := s.snapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	live := declarative.NewResourceSet()
	for _, r := range actual.All() {
		if r.Metadata.Labels[declarative.LabelManagedBy] == ManagedByGitOps {
			live.Add(r)
		}
	}
	topo := &Topology{Edges: declarative.Edges(live), Counts: map[NodeStatus]int{}, Error: syncError, Live: true}
	for _, r := range live.All() {
		if r.Kind == declarative.KindProject {
			continue
		}
		node := TopologyNode{
			Key:    r.Key(),
			Kind:   string(r.Kind),
			Name:   r.Metadata.Name,
			Status: NodeSynced, // live state; no desired to diff against here
		}
		s.resolveLive(workspaceID, r.Kind, r.Metadata.Name, &node)
		topo.Nodes = append(topo.Nodes, node)
		topo.Counts[NodeSynced]++
	}
	return topo, nil
}

func routeURL(rt *models.Route) string {
	if len(rt.Hosts) == 0 || rt.Hosts[0] == "" {
		return ""
	}
	scheme := "https"
	if rt.TLSMode == models.RouteTLSNone {
		scheme = "http"
	}
	u := scheme + "://" + rt.Hosts[0]
	if rt.Path != "" && rt.Path != "/" {
		u += rt.Path
	}
	return u
}

// Empty without an external-access port; exposure via a user Route shows on that Route node.
func (s *Service) appExternalURL(workspaceID uint, app *models.Application) string {
	routes, err := s.routes.ListByApp(workspaceID, app.ID)
	if err != nil {
		return ""
	}
	var fallback *models.Route
	for i := range routes {
		rt := routes[i]
		if !rt.Generated || len(rt.Hosts) == 0 {
			continue
		}
		if rt.TargetPort == app.Port {
			return routeURL(&rt) // the primary external URL
		}
		if fallback == nil {
			fallback = &rt
		}
	}
	if fallback != nil {
		return routeURL(fallback)
	}
	return ""
}

func (s *Service) resolveLive(workspaceID uint, kind declarative.Kind, name string, node *TopologyNode) {
	switch kind {
	case declarative.KindApplication:
		if a, err := s.findApp(workspaceID, name); err == nil {
			node.LiveID, node.Slug, node.Health = a.ID, a.Name, string(a.Status)
			node.URL = s.appExternalURL(workspaceID, a)
		}
	case declarative.KindDatabase:
		if d, err := s.findInstance(workspaceID, name); err == nil {
			node.LiveID, node.Slug, node.Health = d.ID, d.Name, string(d.Status)
		}
	case declarative.KindVolume:
		if v, err := s.findVolume(workspaceID, name); err == nil {
			node.LiveID, node.Slug = v.ID, v.Name
		}
	case declarative.KindStack:
		if st, err := s.findStack(workspaceID, name); err == nil {
			node.LiveID = st.ID
		}
	case declarative.KindRoute:
		if rt, err := s.findRoute(workspaceID, name); err == nil {
			node.LiveID = rt.ID
			node.URL = routeURL(rt)
		}
	case declarative.KindDomain:
		if d, err := s.findDomain(workspaceID, name); err == nil {
			node.LiveID = d.ID
		}
	case declarative.KindSecret, declarative.KindSealedSecret:
		if sec, err := s.findSecret(workspaceID, name); err == nil {
			node.LiveID = sec.ID
		}
	case declarative.KindRegistry:
		if reg, err := s.registries.FindByName(workspaceID, name); err == nil {
			node.LiveID, node.Slug = reg.ID, reg.Name
		}
	case declarative.KindCronJob:
		if s.jobs == nil {
			return
		}
		if cj, err := s.jobs.GetCronJobByName(workspaceID, name); err == nil {
			node.LiveID, node.Slug, node.Health = cj.ID, cj.Name, "scheduled"
			if !cj.Enabled {
				node.Health = "suspended"
			}
		}
	case declarative.KindJob:
		if s.jobs == nil {
			return
		}
		if def, err := s.jobs.GetDefinitionByName(workspaceID, name); err == nil {
			node.LiveID, node.Slug = def.ID, def.Name
			node.Health = string(s.jobs.LastRunStatus(def))
		}
	case declarative.KindGitRepository:
		if s.gitRepos == nil {
			return
		}
		if g, err := s.gitRepos.GetByName(workspaceID, name); err == nil {
			node.LiveID, node.Slug, node.Health = g.ID, g.Name, string(g.ConnectionStatus)
		}
	case declarative.KindGitSource:
		if s.gitSources == nil {
			return
		}
		if sources, err := s.gitSources.List(workspaceID); err == nil {
			for i := range sources {
				if sources[i].Name == name {
					node.LiveID, node.Slug, node.Health = sources[i].ID, sources[i].Name, string(sources[i].Status)
				}
			}
		}
	}
}
