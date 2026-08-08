// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/route"
)

// ProviderHandler serves a remote node's Goma Gateway config over Goma's HTTP provider: a node's
// Gateway polls these endpoints with its join token. The bundle contains every middleware (routes
// reference them by name) but only the routes for apps placed on that node.
type ProviderHandler struct {
	nodes  *node.Service
	routes *route.Service
}

func NewProviderHandler(n *node.Service, routes *route.Service) *ProviderHandler {
	return &ProviderHandler{nodes: n, routes: routes}
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
