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
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/mwcatalog"
	"github.com/miabi-io/miabi/internal/services/audit"
	mwsvc "github.com/miabi-io/miabi/internal/services/middleware"
)

type MiddlewareHandler struct {
	svc   *mwsvc.Service
	audit *audit.Logger
}

func NewMiddlewareHandler(svc *mwsvc.Service, auditLog *audit.Logger) *MiddlewareHandler {
	return &MiddlewareHandler{svc: svc, audit: auditLog}
}

type CreateMiddlewareRequest struct {
	Body struct {
		Name        string                 `json:"name" required:"true"` // unique slug handle (Goma middleware name)
		DisplayName string                 `json:"display_name"`         // free-text label (defaults to name)
		Type        string                 `json:"type" required:"true"`
		Paths       []string               `json:"paths"`
		Rule        map[string]interface{} `json:"rule"`
	} `json:"body"`
}

type UpdateMiddlewareRequest struct {
	Body struct {
		Name  string                 `json:"name"`
		Type  string                 `json:"type"`
		Paths []string               `json:"paths"`
		Rule  map[string]interface{} `json:"rule"`
	} `json:"body"`
}

func (h *MiddlewareHandler) Create(c *okapi.Context, req *CreateMiddlewareRequest) error {
	wsID := middlewares.WorkspaceID(c)
	m, err := h.svc.Create(c.Request().Context(), wsID, mwsvc.Input{
		Name: req.Body.Name, DisplayName: req.Body.DisplayName, Type: req.Body.Type, Paths: req.Body.Paths, Rule: req.Body.Rule,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "middleware.create", m.ID)
	return created(c, redact(m))
}

func (h *MiddlewareHandler) List(c *okapi.Context) error {
	mws, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list middlewares", err)
	}
	out := make([]models.Middleware, len(mws))
	for i := range mws {
		out[i] = redact(&mws[i])
	}
	return ok(c, out)
}

func (h *MiddlewareHandler) Get(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid middleware id")
	}
	m, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("middleware not found")
	}
	return ok(c, redact(m))
}

type MiddlewareCatalog struct {
	Types   []mwcatalog.Descriptor `json:"types"`
	Presets []mwcatalog.Preset     `json:"presets"`
}

func (h *MiddlewareHandler) Catalog(c *okapi.Context) error {
	return ok(c, MiddlewareCatalog{Types: mwcatalog.All(), Presets: mwcatalog.Presets()})
}

func (h *MiddlewareHandler) Update(c *okapi.Context, req *UpdateMiddlewareRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid middleware id")
	}
	wsID := middlewares.WorkspaceID(c)
	m, err := h.svc.Update(c.Request().Context(), wsID, id, mwsvc.Input{
		Name: req.Body.Name, Type: req.Body.Type, Paths: req.Body.Paths, Rule: req.Body.Rule,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "middleware.update", m.ID)
	return ok(c, redact(m))
}

func (h *MiddlewareHandler) Delete(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid middleware id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Delete(c.Request().Context(), wsID, id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "middleware.delete", id)
	return message(c, "middleware deleted")
}

func (h *MiddlewareHandler) id(c *okapi.Context) (uint, error) {
	id, err := strconv.Atoi(c.Param("middlewareID"))
	if err != nil || id <= 0 {
		return 0, errors.New("invalid middleware id")
	}
	return uint(id), nil
}

func (h *MiddlewareHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "middleware", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *MiddlewareHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, mwsvc.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, mwsvc.ErrInvalidRule):
		return c.AbortWithError(422, invalidRuleError{err})
	case errors.Is(err, mwsvc.ErrNameRequired), errors.Is(err, mwsvc.ErrInvalidName), errors.Is(err, mwsvc.ErrTypeRequired):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, mwsvc.ErrNotFound):
		return c.AbortNotFound("middleware not found")
	default:
		return c.AbortInternalServerError("middleware operation failed", err)
	}
}

type invalidRuleError struct{ err error }

func (e invalidRuleError) Error() string { return e.err.Error() }
func (e invalidRuleError) Code() string  { return "MIDDLEWARE_INVALID_RULE" }
func (e invalidRuleError) Unwrap() error { return e.err }

// API responses must never expose stored ciphertext or plaintext secrets.
func redact(m *models.Middleware) models.Middleware {
	cp := *m
	cp.Rule = mwcatalog.Redact(m.Type, m.Rule)
	return cp
}
