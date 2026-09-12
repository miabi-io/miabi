// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
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
		return ok(c, []placement.Location{})
	}
	locs, err := h.placer.svc.Locations(middlewares.WorkspaceID(c), h.placer.admin(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list locations", err)
	}
	return ok(c, locs)
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
