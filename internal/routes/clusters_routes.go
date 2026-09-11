// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/handlers"
)

// clustersRoutes registers the platform admin's cluster inventory. Swarm operations on the default
// cluster stay under /admin/cluster until they become per cluster.
func (r *Router) clustersRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/admin/clusters").WithTagInfo(okapi.GroupTag{Name: "Clusters", Description: "Deploy targets: every node belongs to exactly one cluster."})
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}

	defs := []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/{clusterID}/swarm",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.Status,
			Summary:     "A cluster's swarm status",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{clusterID}/swarm/enable",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.Enable),
			Summary:     "Initialize or adopt a swarm on the cluster's node",
			Request:     &handlers.EnableClusterRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{clusterID}/swarm/disable",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.Disable,
			Summary:     "Take the cluster out of swarm mode",
		},
	}
	return append(defs, append(r.swarmRoutes(g, "/{clusterID}", admin), []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.ListClusters,
			Summary:     "List clusters with their node counts",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{clusterID}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.GetCluster,
			Summary:     "Get a cluster",
		},
		{
			Method:      http.MethodPatch,
			Path:        "/{clusterID}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.UpdateCluster),
			Summary:     "Rename a cluster and set its location code",
			Request:     &handlers.UpdateClusterRequest{},
		},
	}...)...)
}
