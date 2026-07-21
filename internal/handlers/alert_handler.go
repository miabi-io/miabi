// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// AlertHandler serves a workspace's alerts (the shared, deduplicated conditions
// behind the per-user notifications) and their lifecycle transitions.
type AlertHandler struct {
	repo *repositories.AlertRepository
}

func NewAlertHandler(repo *repositories.AlertRepository) *AlertHandler {
	return &AlertHandler{repo: repo}
}

// List returns the workspace's alerts, newest first. ?active=true limits to open
// (firing/acknowledged) alerts.
func (h *AlertHandler) List(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	alerts, err := h.repo.ListByWorkspace(wsID, c.Query("active") == "true", 0)
	if err != nil {
		return c.AbortInternalServerError("failed to list alerts", err)
	}
	return ok(c, alerts)
}

// Acknowledge silences re-notification for an alert while an operator works on it,
// without hiding it. It stays an active condition (still dedup-unique).
func (h *AlertHandler) Acknowledge(c *okapi.Context) error {
	return h.transition(c, models.AlertAcknowledged)
}

// Resolve manually closes an alert (e.g. the operator fixed it out of band before
// the auto-resolve signal arrived).
func (h *AlertHandler) Resolve(c *okapi.Context) error {
	return h.transition(c, models.AlertResolved)
}

func (h *AlertHandler) transition(c *okapi.Context, state models.AlertState) error {
	wsID := middlewares.WorkspaceID(c)
	id, err := uintParam(c, "alertID")
	if err != nil {
		return c.AbortBadRequest("invalid alert id")
	}
	a, err := h.repo.SetState(wsID, id, state)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.AbortNotFound("alert not found")
		}
		return c.AbortInternalServerError("failed to update alert", err)
	}
	return ok(c, a)
}
