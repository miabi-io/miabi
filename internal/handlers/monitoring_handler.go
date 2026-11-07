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
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/monitoring"
)

type MonitoringHandler struct {
	svc *monitoring.Service
}

func NewMonitoringHandler(svc *monitoring.Service) *MonitoringHandler {
	return &MonitoringHandler{svc: svc}
}

// Overview returns a workspace health/resource summary.
func (h *MonitoringHandler) Overview(c *okapi.Context) error {
	ov, err := h.svc.WorkspaceOverview(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to build overview", err)
	}
	return ok(c, ov)
}

// AppMetrics returns a single resource-usage sample for an app.
func (h *MonitoringHandler) AppMetrics(c *okapi.Context) error {
	appID, err := appIDParam(c)
	if err != nil {
		return c.AbortBadRequest("invalid app id")
	}
	sample, err := h.svc.AppMetrics(c.Request().Context(), middlewares.WorkspaceID(c), appID)
	if err != nil {
		// The app is running on an unmanaged node; claiming "no active container" would be false.
		if errors.Is(err, monitoring.ErrTaskOnUnmanagedNode) {
			return c.AbortWithError(409, err)
		}
		if errors.Is(err, monitoring.ErrNoActiveContainer) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to read metrics", err)
	}
	return ok(c, sample)
}

func (h *MonitoringHandler) AppMetricsHistory(c *okapi.Context) error {
	appID, err := appIDParam(c)
	if err != nil {
		return c.AbortBadRequest("invalid app id")
	}
	window := time.Hour
	if q := c.Query("since"); q != "" {
		if d, err := time.ParseDuration(q); err == nil && d > 0 {
			window = d
		}
	}
	limit := 1000
	if q := c.Query("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 5000 {
			limit = n
		}
	}
	samples, err := h.svc.History(middlewares.WorkspaceID(c), appID, time.Now().Add(-window), limit)
	if err != nil {
		return c.AbortInternalServerError("failed to read metrics history", err)
	}
	return ok(c, samples)
}

func (h *MonitoringHandler) WorkspaceUsageLive(c *okapi.Context) error {
	sample, err := h.svc.WorkspaceLiveUsage(c.Request().Context(), middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to read live usage", err)
	}
	return ok(c, sample)
}

func (h *MonitoringHandler) WorkspaceUsageStream(c *okapi.Context) error {
	interval := 3 * time.Second
	if q := c.Query("interval"); q != "" {
		if d, err := time.ParseDuration(q); err == nil {
			interval = d
		}
	}
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	return h.svc.StreamWorkspaceUsage(c.Request().Context(), middlewares.WorkspaceID(c), interval, func(s monitoring.WorkspaceSample) error {
		return c.SSESendJSON(s)
	})
}

func (h *MonitoringHandler) WorkspaceUsageHistory(c *okapi.Context) error {
	window := time.Hour
	if q := c.Query("since"); q != "" {
		if d, err := time.ParseDuration(q); err == nil && d > 0 {
			window = d
		}
	}
	bucket := time.Minute
	if q := c.Query("bucket"); q != "" {
		if d, err := time.ParseDuration(q); err == nil && d > 0 {
			bucket = d
		}
	}
	points, err := h.svc.WorkspaceUsageHistory(middlewares.WorkspaceID(c), time.Now().Add(-window), bucket)
	if err != nil {
		return c.AbortInternalServerError("failed to read usage history", err)
	}
	return ok(c, points)
}

// AppMetricsStream streams live resource-usage samples over SSE.
func (h *MonitoringHandler) AppMetricsStream(c *okapi.Context) error {
	appID, err := appIDParam(c)
	if err != nil {
		return c.AbortBadRequest("invalid app id")
	}
	err = h.svc.StreamAppMetrics(c.Request().Context(), middlewares.WorkspaceID(c), appID, func(s docker.StatsSample) error {
		return c.SSESendJSON(s)
	})
	if errors.Is(err, monitoring.ErrTaskOnUnmanagedNode) || errors.Is(err, monitoring.ErrNoActiveContainer) {
		return c.AbortWithError(409, err)
	}
	return err
}
