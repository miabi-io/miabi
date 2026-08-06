// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
)

// NodeStatus is a resource's sync state relative to the desired manifests,
// derived from the convergence plan.
type NodeStatus string

const (
	NodeSynced    NodeStatus = "synced"      // live state matches desired (noop)
	NodeOutOfSync NodeStatus = "out_of_sync" // exists but drifted (update)
	NodeMissing   NodeStatus = "missing"     // declared but not yet created (create)
	NodeOrphaned  NodeStatus = "orphaned"    // live + GitOps-owned but absent from desired (delete)
)

// TopologyNode is one resource in the project graph: its declarative identity,
// sync status, a link to the live resource (when it exists), and an optional
// runtime health string.
type TopologyNode struct {
	Key    string     `json:"key"`  // "<Kind>/<name>" — stable node id, matches edge endpoints
	Kind   string     `json:"kind"` // Application, Database, Volume, Route, Stack, Secret, Domain
	Name   string     `json:"name"`
	Status NodeStatus `json:"status"`
	// LiveID/Slug point at the live resource so the UI can deep-link to its detail
	// page. LiveID is 0 when the resource is not yet created (missing).
	LiveID uint   `json:"live_id,omitempty"`
	Slug   string `json:"slug,omitempty"`
	// Health is a kind-specific runtime status (e.g. an application's "running"),
	// empty when the kind has no meaningful runtime state or is not live yet.
	Health string `json:"health,omitempty"`
	// URL is the public address a resource is reachable at, when it has one (a
	// Route's host/path with its scheme). Empty for kinds with no public URL.
	URL string `json:"url,omitempty"`
}

// Topology is the resource graph of a manifest bundle: the nodes (resources) and
// the directed edges (dependencies) between them, plus a per-status tally.
type Topology struct {
	Nodes  []TopologyNode     `json:"nodes"`
	Edges  []declarative.Edge `json:"edges"`
	Counts map[NodeStatus]int `json:"counts"`
	// Error, when set, means the desired manifests could not be loaded or parsed
	// (e.g. a broken commit). Nodes/Edges then reflect the last-known live
	// (deployed) state instead of the desired graph, so the view degrades to
	// "what is still running, plus why the latest sync failed" rather than going
	// blank. Empty on a healthy response.
	Error string `json:"error,omitempty"`
	// Live is true when Nodes/Edges came from the live fallback rather than the
	// desired manifests, so the UI can label statuses as last-known rather than
	// authoritative.
	Live bool `json:"live,omitempty"`
}

// statusFromAction maps a plan action to a node sync status.
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

// Topology parses the manifests, diffs them against live state, and returns the
// resource graph for the project-detail view: every declared resource as a node
// (with its sync status and a link to the live resource), plus the dependency
// edges between them. Orphaned GitOps-owned resources (present live but dropped
// from the manifests) are included so the graph shows prune candidates too.
func (s *Service) Topology(ctx context.Context, workspaceID uint, manifests []byte, opts Options) (*Topology, error) {
	// Parse once for edges (env templates must still be intact), then render the
	// same set for an accurate plan — render() rewrites env in place. If the
	// desired manifests are broken, fall back to the live state (the last good
	// sync is still deployed) rather than failing the whole view.
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
	// IncludeNoop so synced resources still appear as nodes; Prune surfaces
	// orphans as delete changes (restricted to this GitOps project's own
	// resources, so a sibling project's apps aren't shown as orphans here).
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
		// Resolve the live resource for deep-linking + health (skip for missing,
		// which has no live counterpart yet).
		if status != NodeMissing {
			s.resolveLive(workspaceID, ch.Kind, ch.Name, &node)
		}
		topo.Nodes = append(topo.Nodes, node)
		topo.Counts[status]++
	}
	return topo, nil
}

// LiveStatus returns the current runtime status of the workspace's stateful
// resources (applications and databases), keyed by topology node key
// ("<Kind>/<slug>"). It is cheap — no git clone and no container inspection — so
// the detail page can poll it to keep node health live between full (cloning)
// topology loads.
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

// LiveTopology builds the graph from live (deployed) state only, used as a
// graceful fallback when the desired manifests cannot be loaded or parsed (a
// broken commit, an unreachable repo). It shows the GitOps-owned resources that
// are still running — the last successful sync — and surfaces syncError so the
// view explains why it is degraded instead of going blank. Edges are derived
// from live specs (structural links like route→app survive; env-template links
// do not, since live env is already resolved).
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

// routeURL builds the public address a route is reachable at: its first host
// with the scheme implied by its TLS mode (http only when TLS is off). Empty
// when the route has no host.
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

// appExternalURL returns the public address of an externally-accessible app: the
// host of its generated external-access route (preferring the one on the app's
// primary port). Empty when the app exposes no external-access port — its
// exposure via a user-declared Route shows on that Route node instead.
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

// resolveLive fills LiveID/Slug/Health from the live resource matching kind+name.
// Best-effort: a lookup miss leaves the node without a link.
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
	case declarative.KindSecret:
		if sec, err := s.findSecret(workspaceID, name); err == nil {
			node.LiveID = sec.ID
		}
	case declarative.KindRegistry:
		if reg, err := s.registries.FindByName(workspaceID, name); err == nil {
			node.LiveID, node.Slug = reg.ID, reg.Name
		}
	}
}
