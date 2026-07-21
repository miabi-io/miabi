// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/handlers"
)

// nodeRoutes registers platform-admin cluster-node management.
func (r *Router) nodeRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/admin/nodes").WithTagInfo(okapi.GroupTag{Name: "Nodes", Description: "Cluster nodes (control plane + agents)."})
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.List,
			Summary:     "List cluster nodes",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/stats",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.Stats,
			Summary:     "Live engine info + resource counts for a node",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/host-metrics",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.HostMetrics,
			Summary:     "Real host CPU & memory usage for the local node (procfs)",
		},
		{
			Method:      http.MethodPost,
			Path:        "",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.Create),
			Summary:     "Add a node (returns a one-time join token)",
			Request:     &handlers.CreateNodeRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/{nodeID}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.Update),
			Summary:     "Update a node's reachability settings",
			Request:     &handlers.CreateNodeRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/workloads",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.Workloads,
			Summary:     "Count apps and databases placed on a node",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/gpus",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.ListGPUs,
			Summary:     "List a node's GPUs",
		},
		{
			Method:      http.MethodPatch,
			Path:        "/{nodeID}/gpus/{gpuID}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.UpdateGPU),
			Summary:     "Enable/disable or set shared on a node GPU",
			Request:     &handlers.UpdateGPURequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/gpus/rescan",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.RescanGPUs,
			Summary:     "Re-run the GPU inventory probe on a node",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/token",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.RegenerateToken,
			Summary:     "Regenerate a node's join token",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/join-command",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.JoinCommand,
			Summary:     "Get the agent join command",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/cordon",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.Cordon,
			Summary:     "Cordon a node (no new placements)",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/uncordon",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.Uncordon,
			Summary:     "Uncordon a node",
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{nodeID}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.Delete,
			Summary:     "Remove a node",
		},

		// Per-node Docker resources (browse the control-plane node and any agent
		// node identically). Removal of platform-managed resources is blocked.
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/containers",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.ContainersList,
			Summary:     "List containers on a node (?all=false for running only)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/container-stats",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.ContainersStats,
			Summary:     "Live resource samples for running containers",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/ports",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.NodePorts,
			Summary:     "List host ports published on the node (across all containers)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/containers/{cid}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.InspectContainer,
			Summary:     "Inspect a container",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/containers",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.RunContainer),
			Summary:     "Run a container",
			Request:     &handlers.RunContainerRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/containers/{cid}/stop",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.StopContainer,
			Summary:     "Stop a container",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/containers/{cid}/restart",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.RestartContainer,
			Summary:     "Restart a container",
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{nodeID}/containers/{cid}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.RemoveContainer,
			Summary:     "Remove a container",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/containers/{cid}/logs",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.ContainerLogs,
			Summary:     "Stream container logs (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/containers/{cid}/stats",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.ContainerStats,
			Summary:     "Stream container stats (SSE)",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/images/pull",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.PullImage),
			Summary:     "Pull an image",
			Request:     &handlers.PullImageRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/networks",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.NetworksList,
			Summary:     "List networks on a node",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/networks",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.CreateNetwork),
			Summary:     "Create a network",
			Request:     &handlers.CreateDockerNetworkRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{nodeID}/networks/{name}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.RemoveNetwork,
			Summary:     "Remove a network",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/volumes",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.VolumesList,
			Summary:     "List volumes on a node",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/volumes",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.CreateVolume),
			Summary:     "Create a volume",
			Request:     &handlers.CreateVolumeRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{nodeID}/volumes/{name}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.RemoveVolume,
			Summary:     "Remove a volume",
		},

		// Import existing (unmanaged) Docker resources into Miabi.
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/importable",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.ImportableResources,
			Summary:     "List unmanaged resources that can be imported",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/import",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.Import),
			Summary:     "Import a selection of existing Docker resources",
			Request:     &handlers.ImportResourcesRequest{},
		},

		// Housekeeping: reclaim disk (dangling images + build cache), reconcile
		// drift (orphans/missing/untracked), and report disk usage. Dry-run first.
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/housekeeping",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.Housekeeping,
			Summary:     "Node housekeeping analysis (disk usage + reclaimable + drift)",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/housekeeping/plan",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.HousekeepingPlan),
			Summary:     "Dry-run a housekeeping selection (preview)",
			Request:     &handlers.HousekeepingSelectionRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/housekeeping/apply",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.HousekeepingApply),
			Summary:     "Apply a housekeeping selection (reclaim and/or remove orphans)",
			Request:     &handlers.HousekeepingSelectionRequest{},
		},

		// Edge gateway (Goma) management for edge-gateway nodes.
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/gateway",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.GatewayState,
			Summary:     "Node gateway status",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/gateway/deploy",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.GatewayDeploy,
			Summary:     "Install / redeploy the node gateway",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/gateway/update",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.GatewayUpdate,
			Summary:     "Safely update the node gateway (test, then promote)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/gateway/events",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.GatewayEvents,
			Summary:     "Stream node gateway update progress (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/gateway/candidates",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.GatewayCandidates,
			Summary:     "List existing gateway containers that can be imported",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/gateway/import",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.GatewayImport),
			Summary:     "Import (adopt) an existing gateway container",
			Request:     &handlers.ImportGatewayRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{nodeID}/gateway/teardown",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.GatewayTeardown,
			Summary:     "Tear down / stop managing the node gateway",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/gateway/logs",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.GatewayLogs,
			Summary:     "Stream node gateway logs (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{nodeID}/gateway/config",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.node.GatewayConfig,
			Summary:     "Get the node gateway config (goma.yml)",
		},
		{
			Method:      http.MethodPut,
			Path:        "/{nodeID}/gateway/config",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.UpdateGatewayConfig),
			Summary:     "Update the node gateway config",
			Request:     &handlers.UpdateNodeGatewayConfigRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/{nodeID}/gateway/image",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.node.UpdateGatewayImage),
			Summary:     "Override the node gateway image/tag",
			Request:     &handlers.UpdateNodeGatewayImageRequest{},
		},
	}
}

// placeableNodeRoutes exposes a minimal node list to any authenticated user so
// the create forms can offer a placement picker. Read-only; no admin fields.
func (r *Router) placeableNodeRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/nodes").WithTagInfo(okapi.GroupTag{Name: "Nodes", Description: "Cluster nodes (control plane + agents)."})
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "",
			Group:       g,
			Middlewares: []okapi.Middleware{r.authenticate},
			Handler:     r.h.node.ListPlaceable,
			Summary:     "List nodes available for placement",
		},
	}
}

// agentRoutes registers the agent connect endpoint. It authenticates by join
// token (not the user JWT) and is registered directly on the app so it bypasses
// the v1 group's auth/maintenance middleware; it is rate-limited per IP.
func (r *Router) agentRoutes() []okapi.RouteDefinition {
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/api/v1/agent/connect",
			Middlewares: []okapi.Middleware{r.agentRateLimit},
			Handler:     r.h.node.Connect,
			Tags:        []string{"Nodes"},
			Summary:     "Agent tunnel (WebSocket; token-authenticated)",
		},
	}
}

// providerRoutes registers the Goma HTTP-provider endpoints a remote node's
// Gateway polls (token-authenticated; bundle = all middlewares + that node's
// routes). Registered directly on the app to bypass the v1 user-auth middleware.
func (r *Router) providerRoutes() []okapi.RouteDefinition {
	return []okapi.RouteDefinition{
		{
			Method:  http.MethodGet,
			Path:    "/api/v1/provider/{slug}",
			Handler: r.h.provider.Full,
			Tags:    []string{"Nodes"},
			Summary: "Node Goma config (routes + middlewares)",
		},
		{
			Method:  http.MethodGet,
			Path:    "/api/v1/provider/routes/{slug}",
			Handler: r.h.provider.Routes,
			Tags:    []string{"Nodes"},
			Summary: "Node Goma routes",
		},
		{
			Method:  http.MethodGet,
			Path:    "/api/v1/provider/middlewares/{slug}",
			Handler: r.h.provider.Middlewares,
			Tags:    []string{"Nodes"},
			Summary: "Node Goma middlewares",
		},
	}
}
