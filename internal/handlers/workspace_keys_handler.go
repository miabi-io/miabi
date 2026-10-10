// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/sealing"
	"github.com/miabi-io/miabi/internal/services/workspacekeys"
)

// WorkspaceKeysHandler exposes a workspace's encryption and sealing keys to its members.
type WorkspaceKeysHandler struct {
	keys    *workspacekeys.Service
	sealing *sealing.Service
	audit   *audit.Logger
}

func NewWorkspaceKeysHandler(keys *workspacekeys.Service, sealingSvc *sealing.Service, auditLog *audit.Logger) *WorkspaceKeysHandler {
	return &WorkspaceKeysHandler{keys: keys, sealing: sealingSvc, audit: auditLog}
}

// SealingKey is the public half of the workspace's active sealing key.
type SealingKey struct {
	Version   int    `json:"version"`
	PublicKey string `json:"public_key"`
}

// Status returns the workspace's key posture: versions, rotation dates and sealing keys.
func (h *WorkspaceKeysHandler) Status(c *okapi.Context) error {
	st, err := h.keys.Status(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to read workspace keys", err)
	}
	return ok(c, st)
}

// SealingKey returns the public key clients seal secrets to. It is public by design, so any member may read it.
func (h *WorkspaceKeysHandler) SealingKey(c *okapi.Context) error {
	k, err := h.sealing.Active(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to read the sealing key", err)
	}
	return ok(c, SealingKey{Version: k.Version, PublicKey: k.PublicKey})
}

// Rotate rotates the workspace's data key and sealing key, at most once per rotation interval.
func (h *WorkspaceKeysHandler) Rotate(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	res, err := h.keys.Rotate(c.Request().Context(), wsID)
	var tooSoon *workspacekeys.TooSoonError
	switch {
	case errors.As(err, &tooSoon):
		return c.AbortWithError(http.StatusConflict, err)
	case errors.Is(err, workspacekeys.ErrUnavailable):
		return c.AbortWithError(http.StatusNotImplemented, err)
	case err != nil && res == nil:
		return c.AbortInternalServerError("key rotation failed", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, WorkspaceID: &wsID, Action: "workspace.rotate_keys",
		TargetType: "workspace", TargetID: strconv.Itoa(int(wsID)), IP: c.RealIP(),
		Metadata: map[string]any{
			"data_key_version": res.DataKeyVersion, "reencrypted": res.Reencrypted,
			"stale_columns": res.StaleColumns, "sealing_key_version": res.SealingKeyVersion,
		},
	})
	if err != nil {
		return c.AbortInternalServerError("key rotation was partial", err)
	}
	return ok(c, res)
}
