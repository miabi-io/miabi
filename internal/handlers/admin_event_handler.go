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
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type AdminEventHandler struct {
	audit *repositories.AuditLogRepository
	users *repositories.UserRepository
	bus   *eventbus.Bus
	ee    enterprise.EE
}

func NewAdminEventHandler(auditRepo *repositories.AuditLogRepository, users *repositories.UserRepository, bus *eventbus.Bus, ee enterprise.EE) *AdminEventHandler {
	return &AdminEventHandler{audit: auditRepo, users: users, bus: bus, ee: ee}
}

func (h *AdminEventHandler) List(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagAuditLog); err != nil {
		return entitlementAbort(c, err)
	}
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	from, to := timeRange(c)
	entries, total, err := h.audit.FindAll(c.Query("search"), c.Query("action"), c.Query("order"), from, to, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list events", err)
	}
	return paginated(c, withActors(h.users, entries), total, page, size)
}

// Get returns a single event.
func (h *AdminEventHandler) Get(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagAuditLog); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid event id")
	}
	entry, err := h.audit.FindByID(uint(id))
	if err != nil {
		return c.AbortNotFound("event not found")
	}
	return ok(c, withActors(h.users, []models.AuditLog{*entry})[0])
}

// Stream pushes live events over SSE as they are recorded.
func (h *AdminEventHandler) Stream(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagAuditLog); err != nil {
		return entitlementAbort(c, err)
	}
	ch, unsubscribe := h.bus.Subscribe(audit.GlobalTopic)
	defer unsubscribe()

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, open := <-ch:
			if !open {
				return nil
			}
			if entry, isLog := e.Data.(*models.AuditLog); isLog && entry != nil {
				e.Data = withActors(h.users, []models.AuditLog{*entry})[0]
			}
			_ = c.SSESendJSON(e)
		}
	}
}
