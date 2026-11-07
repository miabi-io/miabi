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
	"strings"

	"github.com/gorilla/websocket"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/runners"
	"github.com/miabi-io/miabi/internal/services/runner"
	"github.com/miabi-io/runner/proto"
)

// Outbound WebSocket (no inbound ports), authenticated by registration token, rate-limited per IP.
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
			// The Go runner sends no Origin header, so it passes; a cross-site browser Origin is rejected.
			CheckOrigin: allowWSOrigin(nil),
		},
	}
}

func (h *RunnerGatewayHandler) SetAllowedOrigins(origins []string) {
	h.upgrader.CheckOrigin = allowWSOrigin(origins)
}

// Blocks until the runner disconnects, so the HTTP handler holds the connection open.
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
	h.manager.Handle(r, models.RunnerFacts{
		OS: c.Header("X-Runner-OS"), Arch: c.Header("X-Runner-Arch"), Version: c.Header("X-Runner-Version"),
		RemoteIP: c.RealIP(), Features: runnerFeatures(c.Header(proto.HeaderFeatures)),
	}, ws)
	return nil
}

// runnerFeatures splits the comma-separated features a runner advertises.
func runnerFeatures(header string) []string {
	var out []string
	for _, f := range strings.Split(header, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
