// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// AdminEventHandler surfaces the platform-wide audit-log feed (super-admin
// only), backed by the AuditLog table with a live SSE stream. The audit log is
// an Enterprise feature, so every endpoint is gated on the audit_log entitlement.
type AdminEventHandler struct {
	audit *repositories.AuditLogRepository
	users *repositories.UserRepository
	bus   *eventbus.Bus
	ee    enterprise.EE
}

func NewAdminEventHandler(auditRepo *repositories.AuditLogRepository, users *repositories.UserRepository, bus *eventbus.Bus, ee enterprise.EE) *AdminEventHandler {
	return &AdminEventHandler{audit: auditRepo, users: users, bus: bus, ee: ee}
}

// List returns recent platform events, paginated and filterable (page/size,
// optional search/action filters, order=asc|desc).
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
