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
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/image"
)

type ImageHandler struct {
	svc     *image.Service
	remover image.Remover
	audit   *audit.Logger
}

// remover may be nil for DB-only deletion.
func NewImageHandler(svc *image.Service, remover image.Remover, auditLog *audit.Logger) *ImageHandler {
	return &ImageHandler{svc: svc, remover: remover, audit: auditLog}
}

// List returns the workspace catalog, optionally filtered by ?app=<id>.
func (h *ImageHandler) List(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	var appID *uint
	if raw := c.Query("app"); raw != "" {
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil {
			v := uint(id)
			appID = &v
		}
	}
	out, err := h.svc.List(wsID, appID)
	if err != nil {
		return c.AbortInternalServerError("failed to list images", err)
	}
	return ok(c, out)
}

// Get returns a single catalog image.
func (h *ImageHandler) Get(c *okapi.Context) error {
	id, err := uintParam(c, "imageID")
	if err != nil {
		return c.AbortBadRequest("invalid image id")
	}
	img, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("image not found")
	}
	return ok(c, img)
}

func (h *ImageHandler) Delete(c *okapi.Context) error {
	id, err := uintParam(c, "imageID")
	if err != nil {
		return c.AbortBadRequest("invalid image id")
	}
	wsID := middlewares.WorkspaceID(c)
	switch err := h.svc.Delete(c.Request().Context(), wsID, id, h.remover); {
	case err == nil:
		actor := middlewares.UserID(c)
		h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: "image.delete",
			TargetType: "image", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
		return message(c, "image deleted")
	case errors.Is(err, image.ErrNotFound):
		return c.AbortNotFound("image not found")
	case errors.Is(err, image.ErrInUse):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("failed to delete image", err)
	}
}
