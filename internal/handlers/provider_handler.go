// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"net/http"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/services/analytics"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/route"
)

// ProviderHandler serves a remote node's Goma Gateway config over Goma's HTTP provider: a node's
// Gateway polls these endpoints with its join token. The bundle contains every middleware (routes
// reference them by name) but only the routes for apps placed on that node.
type ProviderHandler struct {
	nodes     *node.Service
	routes    *route.Service
	ingest    *analytics.Ingester
	metrics   AnalyticsIngestMetrics
	forwarder func(*models.Server) AnalyticsForwarderConfig
}

// AnalyticsIngestMetrics records ingest outcomes; nil-safe via noopIngestMetrics.
type AnalyticsIngestMetrics interface {
	IngestAccepted(node string, n int)
	IngestRejected(node string, n int)
}

func NewProviderHandler(n *node.Service, routes *route.Service) *ProviderHandler {
	return &ProviderHandler{nodes: n, routes: routes}
}

// SetAnalyticsIngest enables POST /provider/{slug}/analytics. Without it the
// endpoint reports 503 rather than silently dropping a node's events.
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
	// Authenticate by the node's join token or its gateway token (the gateway
	// polls with the latter, which is recoverable for on-demand redeploys).
	srv, err := h.nodes.AuthenticateProvider(c.Param("slug"), bearer(c.Header("Authorization")))
	if err != nil {
		return c.AbortUnauthorized("invalid node token")
	}
	routes, mws, err := h.routes.NodeBundle(srv.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to render node config", err)
	}
	if !withRoutes {
		routes = nil
	}
	if !withMiddlewares {
		mws = nil
	}
	body, err := proxy.RenderBundle(routes, mws)
	if err != nil {
		return c.AbortInternalServerError("failed to render node config", err)
	}
	c.SetHeader("Content-Type", "application/yaml")
	w := c.Response()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}

// AnalyticsForwarderConfig tells an agent how to drain its node's local analytics
// stream. Resolved from the agent's own token, so a node cannot ask about another.
type AnalyticsForwarderConfig struct {
	Enabled       bool   `json:"enabled"`
	Node          string `json:"node"`
	Stream        string `json:"stream,omitempty"`
	RedisAddr     string `json:"redis_addr,omitempty"`
	RedisPassword string `json:"redis_password,omitempty"`
}

// AnalyticsConfig hands the agent its forwarder settings: which local stream its
// gateway writes to and how to reach the node Redis holding it.
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

// IngestAnalytics accepts one batch of request events from a node's gateway and
// appends them to the platform stream, so edge traffic lands in the same rollups
// as the manager's. Authenticated as the node; the node is resolved from the
// token, never from the body.
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
