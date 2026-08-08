// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"github.com/gorilla/websocket"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/runners"
	"github.com/miabi-io/miabi/internal/services/runner"
)

// RunnerGatewayHandler is the runner tunnel endpoint: a runner dials in over an outbound
// WebSocket (NAT-friendly, no inbound ports) authenticated by its registration token, a distinct
// scope from the node join token. It bypasses the user-JWT middleware and is rate-limited per IP.
type RunnerGatewayHandler struct {
	svc      *runner.Service
	manager  *runners.Manager
	upgrader websocket.Upgrader
}

func NewRunnerGatewayHandler(svc *runner.Service, manager *runners.Manager) *RunnerGatewayHandler {
	return &RunnerGatewayHandler{
		svc:     svc,
		manager: manager,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			// The runner is a Go client (no Origin header), so it passes; a browser
			// with a cross-site Origin is rejected. SetAllowedOrigins may widen this
			// to the configured allowlist.
			CheckOrigin: allowWSOrigin(nil),
		},
	}
}

// SetAllowedOrigins restricts browser WebSocket upgrades to same-origin plus the
// given origins (Go runners send no Origin and are unaffected).
func (h *RunnerGatewayHandler) SetAllowedOrigins(origins []string) {
	h.upgrader.CheckOrigin = allowWSOrigin(origins)
}

// Connect authenticates a runner by its registration token, upgrades the
// connection, and hands the tunnel to the manager — blocking until the runner
// disconnects (so the HTTP handler holds the connection open).
func (h *RunnerGatewayHandler) Connect(c *okapi.Context) error {
	token := bearer(c.Header("Authorization"))
	if token == "" {
		token = c.Query("token")
	}
	r, err := h.svc.Authenticate(token)
	if err != nil {
		return c.AbortUnauthorized("invalid runner token")
	}
	ws, err := h.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return nil // upgrade failed; response already handled
	}
	h.manager.Handle(r, c.Header("X-Runner-OS"), c.Header("X-Runner-Arch"), c.Header("X-Runner-Version"), c.RealIP(), ws)
	return nil
}
