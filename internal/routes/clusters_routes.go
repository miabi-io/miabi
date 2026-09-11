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

	return []okapi.RouteDefinition{
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
	}
}
