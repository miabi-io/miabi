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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/services/application"
)

// Uses the host's ps, so it works for images without ps and needs no shell capability.
func (h *ApplicationHandler) Processes(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	list, err := h.svc.Processes(c.Request().Context(), app)
	if err != nil {
		if errors.Is(err, application.ErrTaskOnUnmanagedNode) || errors.Is(err, application.ErrNoActiveContainer) {
			return c.AbortWithError(http.StatusConflict, errors.New("the application has no running container"))
		}
		return c.AbortInternalServerError("failed to list processes", err)
	}
	return ok(c, list)
}

// Any other program would let the exec open an arbitrary binary in the image as a TTY.
var allowedShells = []string{"/bin/sh", "/bin/bash"}

func execShell(requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return allowedShells[0], true
	}
	if !strings.HasPrefix(requested, "/") {
		requested = "/bin/" + requested
	}
	for _, s := range allowedShells {
		if requested == s {
			return s, true
		}
	}
	return "", false
}

// Text frames carry JSON; binary frames are raw stdin.
type execClientMessage struct {
	Type string `json:"type"`           // "stdin" | "resize"
	Data string `json:"data,omitempty"` // stdin payload (for type "stdin")
	Cols uint   `json:"cols,omitempty"` // terminal columns (for type "resize")
	Rows uint   `json:"rows,omitempty"` // terminal rows (for type "resize")
}

// The plan capability is checked before the upgrade so a denial is a real 403, not a dropped socket.
func (h *ApplicationHandler) ExecShell(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	shell, ok := execShell(c.Query("shell"))
	if !ok {
		return c.AbortBadRequest("shell must be one of: " + strings.Join(allowedShells, ", "))
	}
	// Capability gate (before the upgrade, so the client gets a real status).
	if err := h.svc.EnsureExecAllowed(app.WorkspaceID); err != nil {
		if a := quotaAbort(c, err); a != nil {
			return a
		}
		return c.AbortInternalServerError("failed to check shell capability", err)
	}

	ws, err := h.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return nil // Upgrade already wrote an error response.
	}
	defer func() { _ = ws.Close() }()

	h.record(c, app.WorkspaceID, "app.exec", app.ID)

	// The exec session is bound to the connection lifetime.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := h.svc.Exec(ctx, app, docker.ExecOptions{
		Cmd: []string{shell},
		Tty: true,
		Env: []string{"TERM=xterm-256color"},
	})
	if err != nil {
		msg := "failed to start shell: " + err.Error()
		if errors.Is(err, application.ErrTaskOnUnmanagedNode) || errors.Is(err, application.ErrNoActiveContainer) {
			msg = "application has no running container"
		}
		_ = ws.WriteMessage(websocket.TextMessage, []byte("\x1b[31m"+msg+"\x1b[0m\r\n"))
		return nil
	}
	defer func() { _ = stream.Close() }()

	// A TTY stream is raw (no multiplexing), so bytes go straight to the terminal.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := stream.Read(buf)
			if n > 0 {
				if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if rerr != nil {
				_ = ws.WriteMessage(websocket.TextMessage, []byte("\r\n\x1b[33msession closed\x1b[0m\r\n"))
				cancel()
				return
			}
		}
	}()

	// Pump browser input (and resize control frames) -> container stdin.
	for {
		mt, data, rerr := ws.ReadMessage()
		if rerr != nil {
			return nil
		}
		if mt != websocket.TextMessage {
			if _, werr := stream.Write(data); werr != nil {
				return nil
			}
			continue
		}
		var msg execClientMessage
		if json.Unmarshal(data, &msg) != nil {
			// Not JSON: treat the text frame as raw stdin.
			if _, werr := stream.Write(data); werr != nil {
				return nil
			}
			continue
		}
		switch msg.Type {
		case "resize":
			if msg.Rows > 0 && msg.Cols > 0 {
				_ = stream.Resize(ctx, msg.Rows, msg.Cols)
			}
		default: // "stdin" or empty
			if _, werr := stream.Write([]byte(msg.Data)); werr != nil {
				return nil
			}
		}
	}
}
