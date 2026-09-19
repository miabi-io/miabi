// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/network"
)

type NetworkHandler struct {
	svc   *network.Service
	audit *audit.Logger
}

func NewNetworkHandler(svc *network.Service, auditLog *audit.Logger) *NetworkHandler {
	return &NetworkHandler{svc: svc, audit: auditLog}
}

type CreateNetworkRequest struct {
	Body struct {
		Name        string `json:"name" required:"true"` // desired unique slug handle
		DisplayName string `json:"display_name"`         // free-text label (defaults to name)
		Driver      string `json:"driver" enum:"bridge,overlay,macvlan,ipvlan"`
		Internal    bool   `json:"internal"`
	} `json:"body"`
}

func (h *NetworkHandler) Create(c *okapi.Context, req *CreateNetworkRequest) error {
	wsID := middlewares.WorkspaceID(c)
	n, err := h.svc.Create(c.Request().Context(), wsID, network.Input{
		Name: req.Body.Name, DisplayName: req.Body.DisplayName, Driver: req.Body.Driver, Internal: req.Body.Internal,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "network.create", n.ID)
	return created(c, n)
}

func (h *NetworkHandler) List(c *okapi.Context) error {
	nets, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list networks", err)
	}
	return ok(c, nets)
}

// Get returns one network with the addressing the engine actually gave it, including the IPv6 half.
func (h *NetworkHandler) Get(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid network id")
	}
	d, err := h.svc.Detail(c.Request().Context(), middlewares.WorkspaceID(c), id)
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, d)
}

func (h *NetworkHandler) Delete(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid network id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Delete(c.Request().Context(), wsID, id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "network.delete", id)
	return message(c, "network deleted")
}

func (h *NetworkHandler) id(c *okapi.Context) (uint, error) {
	id, err := strconv.Atoi(c.Param("networkID"))
	if err != nil || id <= 0 {
		return 0, errors.New("invalid network id")
	}
	return uint(id), nil
}

func (h *NetworkHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "network", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *NetworkHandler) mapErr(c *okapi.Context, err error) error {
	if a := quotaAbort(c, err); a != nil {
		return a
	}
	switch {
	case errors.Is(err, network.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, network.ErrInUse), errors.Is(err, network.ErrIsDefault):
		return c.AbortWithError(409, err)
	case errors.Is(err, network.ErrNameRequired), errors.Is(err, network.ErrInvalidDriver):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, network.ErrNotFound):
		return c.AbortNotFound("network not found")
	default:
		return c.AbortInternalServerError("network operation failed", err)
	}
}
