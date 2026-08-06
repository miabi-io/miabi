// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/registry"
)

type RegistryHandler struct {
	svc   *registry.Service
	audit *audit.Logger
}

func NewRegistryHandler(svc *registry.Service, auditLog *audit.Logger) *RegistryHandler {
	return &RegistryHandler{svc: svc, audit: auditLog}
}

type CreateRegistryRequest struct {
	Body struct {
		Name     string `json:"name" required:"true"`
		Server   string `json:"server"`
		Username string `json:"username"`
		Secret   string `json:"secret" required:"true"`
	} `json:"body"`
}

type UpdateRegistryRequest struct {
	Body struct {
		Name     string `json:"name"`
		Server   string `json:"server"`
		Username string `json:"username"`
		Secret   string `json:"secret"`
	} `json:"body"`
}

func (h *RegistryHandler) Create(c *okapi.Context, req *CreateRegistryRequest) error {
	wsID := middlewares.WorkspaceID(c)
	reg, err := h.svc.Create(wsID, registry.Input{
		Name: req.Body.Name, Server: req.Body.Server, Username: req.Body.Username, Secret: req.Body.Secret,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "registry.create", reg.ID)
	return created(c, reg)
}

func (h *RegistryHandler) List(c *okapi.Context) error {
	regs, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list registries", err)
	}
	return ok(c, regs)
}

func (h *RegistryHandler) Get(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid registry id")
	}
	reg, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("registry not found")
	}
	return ok(c, reg)
}

func (h *RegistryHandler) Update(c *okapi.Context, req *UpdateRegistryRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid registry id")
	}
	wsID := middlewares.WorkspaceID(c)
	reg, err := h.svc.Update(wsID, id, registry.Input{
		Name: req.Body.Name, Server: req.Body.Server, Username: req.Body.Username, Secret: req.Body.Secret,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "registry.update", reg.ID)
	return ok(c, reg)
}

func (h *RegistryHandler) Delete(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid registry id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Delete(wsID, id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "registry.delete", id)
	return message(c, "registry deleted")
}

func (h *RegistryHandler) Test(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid registry id")
	}
	if err := h.svc.TestConnection(c.Request().Context(), middlewares.WorkspaceID(c), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return c.AbortNotFound("registry not found")
		}
		return c.AbortWithError(400, err)
	}
	return message(c, "authentication succeeded")
}

func (h *RegistryHandler) id(c *okapi.Context) (uint, error) {
	id, err := strconv.Atoi(c.Param("registryID"))
	if err != nil || id <= 0 {
		return 0, errors.New("invalid registry id")
	}
	return uint(id), nil
}

func (h *RegistryHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "registry", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *RegistryHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, registry.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, registry.ErrNameRequired), errors.Is(err, registry.ErrSecretRequired),
		errors.Is(err, registry.ErrUnknownSecret):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, registry.ErrNotFound):
		return c.AbortNotFound("registry not found")
	default:
		return c.AbortInternalServerError("registry operation failed", err)
	}
}
