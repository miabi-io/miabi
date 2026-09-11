// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/handlers"
)

// clusterRoutes registers the default cluster's Swarm management under /admin/cluster. Status works on
// plain Docker too, reporting "not enabled". Every cluster's routes live under /admin/clusters/{clusterID}.
func (r *Router) clusterRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/admin/cluster").WithTagInfo(okapi.GroupTag{Name: "Cluster", Description: "The default cluster's Docker Swarm, opt-in and auto-detected."})
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}

	defs := []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.Status,
			Summary:     "Default cluster swarm status and capability",
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
			Summary:     "Rename the default cluster",
			Request:     &handlers.RenameClusterRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/control-plane-cert",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.ControlPlaneCert,
			Summary:     "The certificate the control plane serves, so agents can be pinned to it",
		},
	}
	return append(defs, r.swarmRoutes(g, "", admin)...)
}

// swarmRoutes are the Swarm operations shared by the default cluster's routes and each cluster's
// routes under /admin/clusters/{clusterID}; prefix carries the cluster path parameter.
func (r *Router) swarmRoutes(g *okapi.Group, prefix string, admin []okapi.Middleware) []okapi.RouteDefinition {
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        prefix + "/nodes",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.Members,
			Summary:     "List swarm nodes (managed + unmanaged members)",
		},
		{
			Method:      http.MethodGet,
			Path:        prefix + "/join-token",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.JoinToken,
			Summary:     "Manual swarm join command + worker token",
		},
		{
			Method:      http.MethodPost,
			Path:        prefix + "/network/apply",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.ApplyNetworking,
			Summary:     "Convert workspace networks to cluster overlays (cross-node east-west)",
		},
		{
			Method:      http.MethodPost,
			Path:        prefix + "/agents",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.DeployAgents),
			Summary:     "Install the Miabi agent on every swarm node (global service)",
			Request:     &handlers.DeployAgentsRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        prefix + "/agents",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.RemoveAgents,
			Summary:     "Remove the cluster agent service (nodes become unmanaged)",
		},
		{
			Method:      http.MethodGet,
			Path:        prefix + "/preflight",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.Preflight,
			Summary:     "Can this cluster's manager run multi-node Swarm, and what must be open?",
		},
		{
			Method:      http.MethodPost,
			Path:        prefix + "/net-check",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.NetCheck,
			Summary:     "Probe the overlay data plane between every pair of nodes (DNS, TCP, 1400-byte payload)",
		},
		{
			Method:      http.MethodPost,
			Path:        prefix + "/members/{swarmNodeID}/availability",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.SetAvailability),
			Summary:     "Set a swarm node's scheduling availability (active | pause | drain)",
			Request:     &handlers.SetAvailabilityRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        prefix + "/members/{swarmNodeID}/tasks",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.NodeTasks,
			Summary:     "List the service tasks the scheduler placed on a swarm node",
		},
		{
			Method:      http.MethodPost,
			Path:        prefix + "/nodes/{nodeID}/join",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.JoinNode,
			Summary:     "Join a node to the cluster",
		},
		{
			Method:      http.MethodPost,
			Path:        prefix + "/nodes/{nodeID}/leave",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.LeaveNode,
			Summary:     "Remove a node from the cluster",
		},
	}
}
