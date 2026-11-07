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
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/placement"
)

// LocationHandler lists the locations a workspace may use and sets its default one.
type LocationHandler struct {
	placer *Placer
	audit  *audit.Logger
}

func NewLocationHandler(placer *Placer, auditLog *audit.Logger) *LocationHandler {
	return &LocationHandler{placer: placer, audit: auditLog}
}

// List returns the locations the workspace may place new resources in.
func (h *LocationHandler) List(c *okapi.Context) error {
	if h.placer == nil || h.placer.svc == nil {
		return ok(c, placement.LocationSet{Locations: []placement.Location{}})
	}
	set, err := h.placer.svc.LocationsFor(middlewares.WorkspaceID(c), h.placer.admin(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list locations", err)
	}
	return ok(c, set)
}

// Empty location means the one a create naming none would land in.
func (h *LocationHandler) Nodes(c *okapi.Context) error {
	if h.placer == nil || h.placer.svc == nil {
		return ok(c, []placement.Node{})
	}
	nodes, err := h.placer.svc.Nodes(middlewares.WorkspaceID(c), c.Query("location"), h.placer.admin(c))
	if err != nil {
		if a := placementAbort(c, err); a != nil {
			return a
		}
		return c.AbortInternalServerError("failed to list nodes", err)
	}
	return ok(c, nodes)
}

// SetDefaultLocationRequest sets where the workspace's new resources land when a create names no location.
type SetDefaultLocationRequest struct {
	Body struct {
		// Location is a location name; empty clears the default.
		Location string `json:"location"`
	} `json:"body"`
}

// SetDefault sets the workspace's default location and returns the updated location list.
func (h *LocationHandler) SetDefault(c *okapi.Context, req *SetDefaultLocationRequest) error {
	if h.placer == nil || h.placer.svc == nil {
		return c.AbortInternalServerError("locations are not available", nil)
	}
	wsID := middlewares.WorkspaceID(c)
	location := strings.TrimSpace(req.Body.Location)
	if err := h.placer.svc.SetDefaultLocation(wsID, location, h.placer.admin(c)); err != nil {
		if errors.Is(err, placement.ErrLocationPinned) {
			return c.AbortWithError(409, err)
		}
		if a := placementAbort(c, err); a != nil {
			return a
		}
		return c.AbortInternalServerError("failed to set the default location", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, WorkspaceID: &wsID, Action: "workspace.default_location",
		TargetType: "workspace", TargetID: strconv.Itoa(int(wsID)), IP: c.RealIP(),
		Metadata: map[string]any{"location": location},
	})
	return h.List(c)
}
