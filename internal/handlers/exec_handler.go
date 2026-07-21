// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/services/application"
)

// Processes returns the running processes in the app's active container (the
// "docker top" view). Sourced from the host's ps, so it works even for images
// with no ps binary and needs no shell capability; the route applies Viewer RBAC.
func (h *ApplicationHandler) Processes(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	psArgs := c.Query("args")
	if psArgs == "" {
		psArgs = "aux"
	}
	list, err := h.svc.Processes(c.Request().Context(), app, psArgs)
	if err != nil {
		if errors.Is(err, application.ErrTaskOnUnmanagedNode) || errors.Is(err, application.ErrNoActiveContainer) {
			return c.AbortWithError(http.StatusConflict, errors.New("the application has no running container"))
		}
		return c.AbortInternalServerError("failed to list processes", err)
	}
	return ok(c, list)
}

// execClientMessage is a control/input frame sent by the browser terminal.
// Text frames carry JSON; binary frames are treated as raw stdin.
type execClientMessage struct {
	Type string `json:"type"`           // "stdin" | "resize"
	Data string `json:"data,omitempty"` // stdin payload (for type "stdin")
	Cols uint   `json:"cols,omitempty"` // terminal columns (for type "resize")
	Rows uint   `json:"rows,omitempty"` // terminal rows (for type "resize")
}

// ExecShell upgrades the request to a WebSocket and bridges it to an
// interactive shell (docker exec) inside the app's active container. The plan
// capability is enforced before the upgrade so a denied request returns a real
// 403 instead of a dropped socket. Authentication and Admin+ RBAC are applied
// by the route middleware.
func (h *ApplicationHandler) ExecShell(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
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

	shell := c.Query("shell")
	if shell == "" {
		shell = "/bin/sh"
	}
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

	// Pump container output -> browser. A TTY stream is raw (no multiplexing),
	// so bytes go straight to the terminal as binary frames.
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
