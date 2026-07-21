// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/handlers"
)

// clusterRoutes registers platform-admin cluster (Docker Swarm) management. The
// status endpoint is always available (reports "not enabled" on plain Docker);
// mutations enable/adopt/disable cluster mode and join/leave nodes.
func (r *Router) clusterRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/admin/cluster").WithTagInfo(okapi.GroupTag{Name: "Cluster", Description: "Cluster networking (Docker Swarm), opt-in and auto-detected."})
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.Status,
			Summary:     "Cluster (swarm) status and capability",
		},
		{
			Method:      http.MethodGet,
			Path:        "/nodes",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.Members,
			Summary:     "List swarm nodes (managed + unmanaged members)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/join-token",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.JoinToken,
			Summary:     "Manual swarm join command + worker token",
		},
		{
			Method:      http.MethodPost,
			Path:        "/enable",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.Enable),
			Summary:     "Enable or adopt cluster mode (swarm init)",
			Request:     &handlers.EnableClusterRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/disable",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.Disable,
			Summary:     "Disable cluster mode (swarm leave)",
		},
		{
			Method:      http.MethodPatch,
			Path:        "",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.Rename),
			Summary:     "Rename the cluster",
			Request:     &handlers.RenameClusterRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/network/apply",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.ApplyNetworking,
			Summary:     "Convert workspace networks to cluster overlays (cross-node east-west)",
		},
		{
			Method:      http.MethodPost,
			Path:        "/agents",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.DeployAgents),
			Summary:     "Install the Miabi agent on every swarm worker (global service)",
			Request:     &handlers.DeployAgentsRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/agents",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.RemoveAgents,
			Summary:     "Remove the cluster agent service (nodes become unmanaged)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/control-plane-cert",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.ControlPlaneCert,
			Summary:     "The certificate the control plane serves, so agents can be pinned to it",
		},
		{
			Method:      http.MethodGet,
			Path:        "/preflight",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.Preflight,
			Summary:     "Can this host run multi-node cluster mode, and what must be open?",
		},
		{
			Method:      http.MethodPost,
			Path:        "/net-check",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.NetCheck,
			Summary:     "Probe the overlay data plane between every pair of nodes (DNS, TCP, 1400-byte payload)",
		},
		{
			Method:      http.MethodPost,
			Path:        "/members/{swarmNodeID}/availability",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.SetAvailability),
			Summary:     "Set a swarm node's scheduling availability (active | pause | drain)",
			Request:     &handlers.SetAvailabilityRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/members/{swarmNodeID}/tasks",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.NodeTasks,
			Summary:     "List the service tasks the scheduler placed on a swarm node",
		},
		{
			Method:      http.MethodPost,
			Path:        "/nodes/{nodeID}/join",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.JoinNode,
			Summary:     "Join a node to the cluster",
		},
		{
			Method:      http.MethodPost,
			Path:        "/nodes/{nodeID}/leave",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.LeaveNode,
			Summary:     "Remove a node from the cluster",
		},
	}
}
