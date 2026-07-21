// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// AnalyticsRollup is one minute-bucketed aggregate of a route's HTTP traffic,
// built by the analytics consumer from Goma's per-request event stream. Rows are
// keyed by (workspace, app, route, minute); queries merge the rows spanning a
// range. It powers the Traffic, Performance and Web Analytics dashboards without
// any per-request storage or app instrumentation.
//
// Latency percentiles come from the histograms (DurationHist/UpstreamHist over
// analytics.LatencyBoundsMs); unique visitors from the mergeable HyperLogLog
// sketch (VisitorsHLL); the Top* maps are bounded top-K counts. None of it holds
// PII (the visitor id is a daily-salted hash produced at the edge; no IP).
type AnalyticsRollup struct {
	ID            uint      `gorm:"primaryKey" json:"-"`
	WorkspaceID   uint      `gorm:"uniqueIndex:idx_arollup,priority:1;not null" json:"workspace_id"`
	ApplicationID uint      `gorm:"uniqueIndex:idx_arollup,priority:2;index" json:"application_id"`
	RouteName     string    `gorm:"uniqueIndex:idx_arollup,priority:3;size:200" json:"route_name"`
	Bucket        time.Time `gorm:"uniqueIndex:idx_arollup,priority:4;index" json:"bucket"` // minute-truncated UTC

	Requests  int64 `json:"requests"`
	BytesIn   int64 `json:"bytes_in"`
	BytesOut  int64 `json:"bytes_out"`
	Status2xx int64 `json:"status_2xx"`
	Status3xx int64 `json:"status_3xx"`
	Status4xx int64 `json:"status_4xx"`
	Status5xx int64 `json:"status_5xx"`

	// Latency histograms (per-bucket counts over analytics.LatencyBoundsMs, with a
	// trailing overflow bucket) + sums for the mean.
	DurationHist []int64 `gorm:"serializer:json" json:"-"`
	UpstreamHist []int64 `gorm:"serializer:json" json:"-"`
	DurationSum  int64   `json:"-"` // milliseconds
	UpstreamSum  int64   `json:"-"`

	// VisitorsHLL is the serialized HyperLogLog sketch of visitor ids for this
	// bucket — mergeable, so a range's uniques is the merge of its buckets.
	VisitorsHLL []byte `json:"-"`

	// Bounded top-K count maps for the categorical breakdowns.
	TopPaths      map[string]int64 `gorm:"serializer:json" json:"-"`
	TopReferrers  map[string]int64 `gorm:"serializer:json" json:"-"`
	TopCountries  map[string]int64 `gorm:"serializer:json" json:"-"`
	TopUAFamilies map[string]int64 `gorm:"serializer:json" json:"-"` // browser family
	TopOS         map[string]int64 `gorm:"serializer:json" json:"-"`
	TopDevice     map[string]int64 `gorm:"serializer:json" json:"-"` // desktop | mobile | tablet | bot
	TopMethods    map[string]int64 `gorm:"serializer:json" json:"-"`

	// BotRequests is the subset of Requests classified as automated (crawlers,
	// scripts) — the rest are treated as human, for the bot-vs-human split.
	BotRequests int64 `json:"-"`
}
