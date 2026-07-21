// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"encoding/csv"
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

// AnalyticsHandler serves the Workspace Analytics dashboards (Traffic,
// Performance, Web Analytics) from the minute rollups the consumer builds. The
// dashboards are open-source; the edition (ee) only bounds the retention window
// and gates CSV export.
type AnalyticsHandler struct {
	repo *repositories.AnalyticsRepository
	ee   enterprise.EE
}

func NewAnalyticsHandler(repo *repositories.AnalyticsRepository, ee enterprise.EE) *AnalyticsHandler {
	return &AnalyticsHandler{repo: repo, ee: ee}
}

// maxAnalyticsRange caps how far back a single query may look, keeping the
// row scan bounded regardless of the requested window.
const maxAnalyticsRange = 90 * 24 * time.Hour

// retentionCap returns the entitlement retention cap in days (-1 = unlimited),
// and maxWindow bounds a requested window to that cap so a Community install can
// only look back within its retention (extended retention is an EE entitlement).
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

// Report returns the combined analytics report for the workspace over a range.
// Query params:
//
//	range = window ending now (e.g. 15m, 1h, 24h, 7d, 30d); default 24h
//	app   = optional application id to filter to a single app
func (h *AnalyticsHandler) Report(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)

	window := parseRange(c.Query("range"), 24*time.Hour)
	if max := h.maxWindow(); window > max {
		window = max
	}
	until := time.Now().UTC()
	since := until.Add(-window)

	appFilter, err := appFilterParam(c)
	if err != nil {
		return c.AbortBadRequest("invalid app id")
	}

	rows, err := h.repo.Range(wsID, appFilter, since, until)
	if err != nil {
		return c.AbortInternalServerError("failed to read analytics", err)
	}
	report := analytics.BuildReport(rows, since, until)
	report.RetentionDays = h.retentionCap()
	report.Exportable = h.ee.Has(enterprise.FlagAnalyticsExport)

	// Period-over-period: totals of the immediately preceding, equal-length window
	// power the overview's delta indicators. Skipped on the widest window to bound
	// the scan, and best-effort (a failure just omits the comparison).
	if window <= 30*24*time.Hour {
		if prev, err := h.repo.Range(wsID, appFilter, since.Add(-window), since); err == nil {
			prevTotals := analytics.BuildReport(prev, since.Add(-window), since).Totals
			report.Compare = &prevTotals
		}
	}
	return ok(c, report)
}

// Export streams the analytics time series as CSV. Enterprise-gated
// (analytics_export) — 402/403 in Community.
func (h *AnalyticsHandler) Export(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagAnalyticsExport); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	window := parseRange(c.Query("range"), 24*time.Hour)
	if window > maxAnalyticsRange {
		window = maxAnalyticsRange
	}
	until := time.Now().UTC()
	since := until.Add(-window)

	appFilter, err := appFilterParam(c)
	if err != nil {
		return c.AbortBadRequest("invalid app id")
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

// appFilterParam reads an optional ?app= application id (nil when absent).
func appFilterParam(c *okapi.Context) (*uint, error) {
	q := c.Query("app")
	if q == "" {
		return nil, nil
	}
	id, err := strconv.ParseUint(q, 10, 64)
	if err != nil || id == 0 {
		return nil, err
	}
	v := uint(id)
	return &v, nil
}

// Apps lists the application ids that have analytics data in the window, so the
// UI's app filter shows only apps with traffic.
func (h *AnalyticsHandler) Apps(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	window := parseRange(c.Query("range"), 24*time.Hour)
	if max := h.maxWindow(); window > max {
		window = max
	}
	until := time.Now().UTC()
	ids, err := h.repo.AppIDs(wsID, until.Add(-window), until)
	if err != nil {
		return c.AbortInternalServerError("failed to list analytics apps", err)
	}
	if ids == nil {
		ids = []uint{}
	}
	return ok(c, map[string]any{"application_ids": ids})
}

// parseRange parses a window string like "15m", "2h", "7d". Bare "d" days are
// supported (time.ParseDuration can't). Falls back to def on anything invalid.
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
