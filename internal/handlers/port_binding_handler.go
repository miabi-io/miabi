// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/portbinding"
)

type PortBindingHandler struct {
	svc   *portbinding.Service
	audit *audit.Logger
}

func NewPortBindingHandler(svc *portbinding.Service, auditLog *audit.Logger) *PortBindingHandler {
	return &PortBindingHandler{svc: svc, audit: auditLog}
}

type RequestPortBindingRequest struct {
	Body struct {
		ApplicationID uint   `json:"application_id" required:"true"`
		ContainerPort int    `json:"container_port" required:"true" min:"1" max:"65535"`
		Protocol      string `json:"protocol" enum:"tcp,udp"`
		HostPort      int    `json:"host_port" required:"true" min:"1" max:"65535"`
	} `json:"body"`
}

type ReviewPortBindingRequest struct {
	Body struct {
		Note string `json:"note"`
	} `json:"body"`
}

func (h *PortBindingHandler) Request(c *okapi.Context, req *RequestPortBindingRequest) error {
	wsID := middlewares.WorkspaceID(c)
	b, err := h.svc.Request(wsID, middlewares.UserID(c), portbinding.RequestInput{
		ApplicationID: req.Body.ApplicationID, ContainerPort: req.Body.ContainerPort,
		Protocol: req.Body.Protocol, HostPort: req.Body.HostPort,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, &wsID, "port_binding.request", b.ID)
	return created(c, b)
}

// Suggest returns a free host port on the app's node, to pre-fill the form when
// a chosen port conflicts. Query: application_id (required), protocol, preferred.
func (h *PortBindingHandler) Suggest(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	appID, _ := strconv.Atoi(c.Query("application_id"))
	if appID <= 0 {
		return c.AbortBadRequest("application_id is required")
	}
	preferred, _ := strconv.Atoi(c.Query("preferred"))
	port, err := h.svc.SuggestHostPort(wsID, uint(appID), c.Query("protocol"), preferred)
	if err != nil {
		return h.mapErr(c, err)
	}
	if port == 0 {
		return c.AbortWithError(409, errors.New("no free host port available in the allowed range"))
	}
	return ok(c, map[string]int{"host_port": port})
}

func (h *PortBindingHandler) List(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if appQ := c.Query("application_id"); appQ != "" {
		appID, _ := strconv.Atoi(appQ)
		list, err := h.svc.ListByApp(wsID, uint(appID))
		if err != nil {
			return c.AbortInternalServerError("failed to list port bindings", err)
		}
		return ok(c, list)
	}
	list, err := h.svc.ListByWorkspace(wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to list port bindings", err)
	}
	return ok(c, list)
}

func (h *PortBindingHandler) Cancel(c *okapi.Context) error {
	id, err := bindingID(c)
	if err != nil {
		return c.AbortBadRequest("invalid binding id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Cancel(wsID, id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, &wsID, "port_binding.cancel", id)
	return message(c, "port binding cancelled")
}

func (h *PortBindingHandler) AdminList(c *okapi.Context) error {
	status := models.PortBindingStatus(c.Query("status"))
	if status == "" {
		status = models.PortBindingPending
	}
	list, err := h.svc.ListByStatus(status)
	if err != nil {
		return c.AbortInternalServerError("failed to list port bindings", err)
	}
	return ok(c, list)
}

func (h *PortBindingHandler) Approve(c *okapi.Context, req *ReviewPortBindingRequest) error {
	id, err := bindingID(c)
	if err != nil {
		return c.AbortBadRequest("invalid binding id")
	}
	b, err := h.svc.Approve(id, middlewares.UserID(c), req.Body.Note)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, &b.WorkspaceID, "port_binding.approve", b.ID)
	return ok(c, b)
}

func (h *PortBindingHandler) Reject(c *okapi.Context, req *ReviewPortBindingRequest) error {
	id, err := bindingID(c)
	if err != nil {
		return c.AbortBadRequest("invalid binding id")
	}
	b, err := h.svc.Reject(id, middlewares.UserID(c), req.Body.Note)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, &b.WorkspaceID, "port_binding.reject", b.ID)
	return ok(c, b)
}

func bindingID(c *okapi.Context) (uint, error) {
	id, err := strconv.Atoi(c.Param("bindingID"))
	if err != nil || id <= 0 {
		return 0, errors.New("invalid binding id")
	}
	return uint(id), nil
}

func (h *PortBindingHandler) record(c *okapi.Context, wsID *uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: wsID, Action: action, TargetType: "port_binding", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *PortBindingHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, portbinding.ErrHostPortTaken), errors.Is(err, portbinding.ErrManagedBinding):
		return c.AbortWithError(409, err)
	case errors.Is(err, portbinding.ErrPortNotExposed), errors.Is(err, portbinding.ErrHostPortRange), errors.Is(err, portbinding.ErrNotPending):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, portbinding.ErrAppRequired):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, portbinding.ErrNotFound):
		return c.AbortNotFound("port binding not found")
	default:
		return c.AbortInternalServerError("port binding operation failed", err)
	}
}
