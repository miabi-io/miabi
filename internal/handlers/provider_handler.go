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

package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/services/analytics"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/gatewayinstance"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/route"
)

// The bundle carries every middleware (routes reference them by name) but only this node's routes.
type ProviderHandler struct {
	nodes     *node.Service
	routes    *route.Service
	ingest    *analytics.Ingester
	metrics   AnalyticsIngestMetrics
	forwarder func(*models.Server) AnalyticsForwarderConfig
	clusters  ClusterGateways
	instances *gatewayinstance.Service
	gwNodes   GatewayNodes
}

// GatewayNodes resolves the node a gateway names itself after, so the claim is checked against the cluster.
type GatewayNodes interface {
	FindByName(name string) (*models.Server, error)
}

func (h *ProviderHandler) SetGatewayInstances(i *gatewayinstance.Service, nodes GatewayNodes) {
	h.instances, h.gwNodes = i, nodes
}

type ClusterGateways interface {
	AuthenticateGateway(name, token string) (*models.Cluster, error)
}

func (h *ProviderHandler) SetClusterGateways(c ClusterGateways) { h.clusters = c }

// AnalyticsIngestMetrics records ingest outcomes; nil-safe via noopIngestMetrics.
type AnalyticsIngestMetrics interface {
	IngestAccepted(node string, n int)
	IngestRejected(node string, n int)
}

func NewProviderHandler(n *node.Service, routes *route.Service) *ProviderHandler {
	return &ProviderHandler{nodes: n, routes: routes}
}

// Without it the endpoint reports 503 rather than silently dropping a node's events.
func (h *ProviderHandler) SetAnalyticsIngest(i *analytics.Ingester, m AnalyticsIngestMetrics,
	forwarder func(*models.Server) AnalyticsForwarderConfig) {
	h.ingest, h.metrics, h.forwarder = i, m, forwarder
}

// Full serves routes + middlewares.
func (h *ProviderHandler) Full(c *okapi.Context) error { return h.serve(c, true, true) }

// Routes serves only this node's routes.
func (h *ProviderHandler) Routes(c *okapi.Context) error { return h.serve(c, true, false) }

// Middlewares serves all middlewares.
func (h *ProviderHandler) Middlewares(c *okapi.Context) error { return h.serve(c, false, true) }

func (h *ProviderHandler) serve(c *okapi.Context, withRoutes, withMiddlewares bool) error {
	// The gateway polls with its gateway token, which is recoverable for on-demand redeploys.
	srv, err := h.nodes.AuthenticateProvider(c.Param("slug"), bearer(c.Header("Authorization")))
	if err != nil {
		return c.AbortUnauthorized("invalid node token")
	}
	routes, mws, err := h.routes.NodeBundle(srv.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to render node config", err)
	}
	poll := gatewayinstance.Instance{Gateway: srv.Name, NodeID: srv.ID, Endpoint: gatewayinstance.EndpointNode}
	// Under this node's key so no other node's gateway can read the bundle.
	return h.writeBundle(c, routes, mws, withRoutes, withMiddlewares, edgegateway.ConfigKey(srv, proxy.CentralConfigKey()), srv.ClusterID, poll)
}

func (h *ProviderHandler) ClusterFull(c *okapi.Context) error { return h.serveCluster(c, true, true) }

func (h *ProviderHandler) ClusterRoutes(c *okapi.Context) error {
	return h.serveCluster(c, true, false)
}

func (h *ProviderHandler) ClusterMiddlewares(c *okapi.Context) error {
	return h.serveCluster(c, false, true)
}

func (h *ProviderHandler) serveCluster(c *okapi.Context, withRoutes, withMiddlewares bool) error {
	if h.clusters == nil {
		return c.AbortUnauthorized("invalid cluster gateway token")
	}
	cl, err := h.clusters.AuthenticateGateway(c.Param("cluster"), bearer(c.Header("Authorization")))
	if err != nil {
		return c.AbortUnauthorized("invalid cluster gateway token")
	}
	routes, mws, err := h.routes.ClusterBundle(cl)
	if err != nil {
		return c.AbortInternalServerError("failed to render cluster config", err)
	}
	return h.writeBundle(c, routes, mws, withRoutes, withMiddlewares, edgegateway.ClusterConfigKey(cl, proxy.CentralConfigKey()), cl.ID, h.clusterPoll(c, cl))
}

// The name is the gateway's own claim, kept only when it names a node of this cluster.
func (h *ProviderHandler) clusterPoll(c *okapi.Context, cl *models.Cluster) gatewayinstance.Instance {
	poll := gatewayinstance.Instance{Gateway: "ip:" + c.RealIP(), Endpoint: gatewayinstance.EndpointCluster}
	name := strings.TrimSpace(c.Header(edgegateway.GatewayIDHeader))
	if name == "" || h.gwNodes == nil {
		return poll
	}
	if srv, err := h.gwNodes.FindByName(name); err == nil && srv.ClusterID == cl.ID {
		poll.Gateway, poll.NodeID = srv.Name, srv.ID
	}
	return poll
}

func (h *ProviderHandler) writeBundle(c *okapi.Context, routes []proxy.RenderedRoute, mws []proxy.RenderedMiddleware,
	withRoutes, withMiddlewares bool, key string, clusterID uint, poll gatewayinstance.Instance) error {
	if !withRoutes {
		routes = nil
	}
	if !withMiddlewares {
		mws = nil
	}
	body, sum, err := proxy.RenderProviderBundle(routes, mws, key)
	if err != nil {
		return c.AbortInternalServerError("failed to render gateway config", err)
	}
	// Goma's provider fetches the full bundle; the partial endpoints are for people, not gateways.
	if withRoutes && withMiddlewares {
		poll.Checksum, poll.RemoteIP = sum, c.RealIP()
		poll.Version = gatewayinstance.GomaVersion(c.Header("User-Agent"))
		h.instances.Record(c.Request().Context(), clusterID, poll)
	}
	if c.Header("If-None-Match") == sum {
		c.Response().WriteHeader(http.StatusNotModified)
		return nil
	}
	c.SetHeader("Content-Type", "application/yaml")
	w := c.Response()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}

// Resolved from the agent's own token, so a node cannot ask about another.
type AnalyticsForwarderConfig struct {
	Enabled       bool   `json:"enabled"`
	Node          string `json:"node"`
	Stream        string `json:"stream,omitempty"`
	RedisAddr     string `json:"redis_addr,omitempty"`
	RedisPassword string `json:"redis_password,omitempty"`
}

func (h *ProviderHandler) AnalyticsConfig(c *okapi.Context) error {
	srv, err := h.nodes.Authenticate(bearer(c.Header("Authorization")))
	if err != nil {
		return c.AbortUnauthorized("invalid node token")
	}
	if h.ingest == nil || h.forwarder == nil {
		return ok(c, AnalyticsForwarderConfig{Node: srv.Name})
	}
	cfg := h.forwarder(srv)
	cfg.Node = srv.Name
	return ok(c, cfg)
}

// AnalyticsIngestRequest is a batch of gateway events forwarded by an edge node.
type AnalyticsIngestRequest struct {
	Body analytics.Batch `json:"body"`
}

// The node is resolved from the token, never from the body.
func (h *ProviderHandler) IngestAnalytics(c *okapi.Context, req *AnalyticsIngestRequest) error {
	if h.ingest == nil {
		return c.AbortWithError(http.StatusServiceUnavailable, errAnalyticsDisabled)
	}
	srv, err := h.nodes.AuthenticateProvider(c.Param("slug"), bearer(c.Header("Authorization")))
	if err != nil {
		return c.AbortUnauthorized("invalid node token")
	}
	res, err := h.ingest.Ingest(c.Request().Context(), srv.ID, req.Body)
	switch {
	case errors.Is(err, analytics.ErrBatchTooLarge), errors.Is(err, analytics.ErrNoEvents), errors.Is(err, analytics.ErrNoBatchID):
		// Permanent: the forwarder must drop this batch, not retry it forever.
		return c.AbortBadRequest(err.Error())
	case err != nil:
		return c.AbortInternalServerError("failed to ingest analytics", err)
	}
	if h.metrics != nil {
		h.metrics.IngestAccepted(srv.Name, res.Accepted)
		if res.Rejected > 0 {
			h.metrics.IngestRejected(srv.Name, res.Rejected)
		}
	}
	if res.Rejected > 0 {
		logger.Warn("analytics ingest dropped events for routes the node does not serve",
			"node", srv.Name, "rejected", res.Rejected, "accepted", res.Accepted)
	}
	return ok(c, res)
}

var errAnalyticsDisabled = errors.New("analytics ingest is not enabled on this instance")
