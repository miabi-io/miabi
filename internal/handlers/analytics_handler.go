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
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/analytics"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// The dashboards are open-source; the edition only bounds retention and gates CSV export.
type AnalyticsHandler struct {
	repo *repositories.AnalyticsRepository
	ee   enterprise.EE
	// nil when Redis isn't configured; the endpoint then reports zero rather than failing.
	live *analytics.LiveTracker
	// apps resolves an ?app= given as a uid or name; nil accepts numeric ids only.
	apps middlewares.AppRefResolver
}

func NewAnalyticsHandler(repo *repositories.AnalyticsRepository, ee enterprise.EE, live *analytics.LiveTracker, apps middlewares.AppRefResolver) *AnalyticsHandler {
	return &AnalyticsHandler{repo: repo, ee: ee, live: live, apps: apps}
}

const maxAnalyticsRange = 90 * 24 * time.Hour

// Retention beyond the Community window is an EE entitlement.
func (h *AnalyticsHandler) retentionCap() int {
	return h.ee.Entitlements().AnalyticsRetentionDays()
}

func (h *AnalyticsHandler) maxWindow() time.Duration {
	capDays := h.retentionCap()
	if capDays < 0 {
		return maxAnalyticsRange
	}
	if w := time.Duration(capDays) * 24 * time.Hour; w < maxAnalyticsRange {
		return w
	}
	return maxAnalyticsRange
}

// range is a window ending now (15m..30d, default 24h) or "<from>..<to>"; app filters to one app.
func (h *AnalyticsHandler) Report(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)

	since, until, err := resolveWindow(c.Query("range"), time.Now().UTC(), h.maxWindow())
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	window := until.Sub(since)

	appFilter, err := h.appFilterParam(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}

	rows, err := h.repo.Range(wsID, appFilter, since, until)
	if err != nil {
		return c.AbortInternalServerError("failed to read analytics", err)
	}
	report := analytics.BuildReport(rows, since, until)
	report.RetentionDays = h.retentionCap()
	report.Exportable = h.ee.Has(enterprise.FlagAnalyticsExport)

	// Skipped on the widest window to bound the scan; best-effort.
	if window <= 30*24*time.Hour {
		if prev, err := h.repo.Range(wsID, appFilter, since.Add(-window), since); err == nil {
			prevTotals := analytics.BuildReport(prev, since.Add(-window), since).Totals
			report.Compare = &prevTotals
		}
	}
	return ok(c, report)
}

func (h *AnalyticsHandler) Export(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagAnalyticsExport); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	since, until, err := resolveWindow(c.Query("range"), time.Now().UTC(), maxAnalyticsRange)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}

	appFilter, err := h.appFilterParam(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}

	rows, err := h.repo.Range(wsID, appFilter, since, until)
	if err != nil {
		return c.AbortInternalServerError("failed to read analytics", err)
	}
	report := analytics.BuildReport(rows, since, until)

	w := c.ResponseWriter()
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="analytics.csv"`)
	w.WriteHeader(http.StatusOK)
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"time", "requests", "errors", "bytes_in", "bytes_out", "unique_visitors", "avg_latency_ms", "p95_latency_ms"})
	for _, p := range report.Series {
		_ = cw.Write([]string{
			p.T.Format(time.RFC3339),
			strconv.FormatInt(p.Requests, 10),
			strconv.FormatInt(p.Errors, 10),
			strconv.FormatInt(p.BytesIn, 10),
			strconv.FormatInt(p.BytesOut, 10),
			strconv.FormatInt(p.Uniques, 10),
			strconv.FormatFloat(p.AvgLatency, 'f', 1, 64),
			strconv.FormatFloat(p.P95Latency, 'f', 1, 64),
		})
	}
	return nil
}

// Summary reads fewer columns than Report, so the traffic card costs a fraction of a full report.
func (h *AnalyticsHandler) Summary(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)

	since, until, err := resolveWindow(c.Query("range"), time.Now().UTC(), h.maxWindow())
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	window := until.Sub(since)

	appFilter, err := h.appFilterParam(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}

	rows, err := h.repo.RangeSummary(wsID, appFilter, since, until, true)
	if err != nil {
		return c.AbortInternalServerError("failed to read analytics", err)
	}
	summary := analytics.BuildSummary(rows, since, until)

	// Skipped on the widest window to bound the scan; best-effort, and read without the country top-K.
	if window <= 30*24*time.Hour {
		if prev, err := h.repo.RangeSummary(wsID, appFilter, since.Add(-window), since, false); err == nil {
			prevTotals := analytics.BuildSummary(prev, since.Add(-window), since).Totals
			summary.Compare = &prevTotals
		}
	}
	return ok(c, summary)
}

// Polled by the dashboard, so it stays a single cheap Redis read and never touches the database.
func (h *AnalyticsHandler) Live(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)

	appFilter, err := h.appFilterParam(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	var app uint
	if appFilter != nil {
		app = *appFilter
	}

	window := analytics.DefaultLiveWindow
	if h.live != nil {
		window = h.live.Window()
	}
	// A missing Redis means nobody counted, not an error: the rest of the dashboard reads Postgres.
	count, err := h.live.Count(c.Request().Context(), wsID, app)
	if err != nil {
		count = 0
	}
	return ok(c, map[string]any{
		"visitors":       count,
		"window_seconds": int(window.Seconds()),
	})
}

// A numeric id is used without lookup, so a deleted app's traffic stays filterable.
func (h *AnalyticsHandler) appFilterParam(c *okapi.Context) (*uint, error) {
	q := strings.TrimSpace(c.Query("app"))
	if q == "" {
		return nil, nil
	}
	if id, err := strconv.ParseUint(q, 10, 64); err == nil && id > 0 {
		v := uint(id)
		return &v, nil
	}
	if h.apps == nil {
		return nil, errAppNotFound
	}
	id, err := h.apps.IDByRef(middlewares.WorkspaceID(c), q)
	if err != nil || id == 0 {
		return nil, errAppNotFound
	}
	return &id, nil
}

var errAppNotFound = errors.New("application not found")

func (h *AnalyticsHandler) Apps(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	since, until, err := resolveWindow(c.Query("range"), time.Now().UTC(), h.maxWindow())
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	ids, err := h.repo.AppIDs(wsID, since, until)
	if err != nil {
		return c.AbortInternalServerError("failed to list analytics apps", err)
	}
	if ids == nil {
		ids = []uint{}
	}
	return ok(c, map[string]any{"application_ids": ids})
}

// Supports bare "d" days, which time.ParseDuration can't; falls back to def on invalid input.
func parseRange(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
		return def
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

// Absolute ranges are "<from>..<to>" (RFC 3339 or YYYY-MM-DD), to exclusive and clamped to now.
func resolveWindow(raw string, now time.Time, max time.Duration) (since, until time.Time, err error) {
	from, to, absolute := strings.Cut(strings.TrimSpace(raw), "..")
	if !absolute {
		window := parseRange(raw, 24*time.Hour)
		if window > max {
			window = max
		}
		return now.Add(-window), now, nil
	}
	if since, err = parseRangeBound(from); err != nil {
		return since, until, err
	}
	if until, err = parseRangeBound(to); err != nil {
		return since, until, err
	}
	if until.After(now) {
		until = now
	}
	if floor := now.Add(-max); since.Before(floor) {
		since = floor
	}
	if !since.Before(until) {
		return since, until, errors.New("range: the start must be before the end and within the retention window")
	}
	return since, until, nil
}

func parseRangeBound(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("range: %q is not an RFC 3339 time or a YYYY-MM-DD date", s)
}
