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

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

type AlertHandler struct {
	repo *repositories.AlertRepository
}

func NewAlertHandler(repo *repositories.AlertRepository) *AlertHandler {
	return &AlertHandler{repo: repo}
}

func (h *AlertHandler) List(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	alerts, err := h.repo.ListByWorkspace(wsID, c.Query("active") == "true", 0)
	if err != nil {
		return c.AbortInternalServerError("failed to list alerts", err)
	}
	return ok(c, alerts)
}

// Acknowledge silences re-notification without hiding the alert; it stays active and dedup-unique.
func (h *AlertHandler) Acknowledge(c *okapi.Context) error {
	return h.transition(c, models.AlertAcknowledged)
}

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
