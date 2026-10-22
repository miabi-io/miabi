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

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
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
			Method:      http.MethodGet,
			Path:        "/{clusterID}/external-certificates",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.ExternalCertificates,
			Summary:     "System-workspace certificates that could serve this cluster's generated app URLs",
			Response:    &dto.Response[[]handlers.ExternalCertificateOption]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/{clusterID}/dedication",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.cluster.Dedication,
			Summary:     "What a change of owning organization would affect in this location",
		},
		{
			Method:      http.MethodPatch,
			Path:        "/{clusterID}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.UpdateCluster),
			Summary:     "Rename a cluster, set its location code, visibility or cordon",
			Request:     &handlers.UpdateClusterRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/{clusterID}/gateway",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.SetClusterGateway),
			Summary:     "Pick the node that serves a swarm cluster's routes",
			Request:     &handlers.SetClusterGatewayRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{clusterID}/convert",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.cluster.ConvertIngress),
			Summary:     "Give a standalone cluster's node a gateway: its own, or a swarm's",
			Request:     &handlers.ConvertIngressRequest{},
		},
	}...)...)
}
