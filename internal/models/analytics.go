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

package models

import "time"

// Holds no PII.
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

	// Per-bucket counts over analytics.LatencyBoundsMs, with a trailing overflow bucket.
	DurationHist []int64 `gorm:"serializer:json" json:"-"`
	UpstreamHist []int64 `gorm:"serializer:json" json:"-"`
	DurationSum  int64   `json:"-"` // milliseconds
	UpstreamSum  int64   `json:"-"`

	// Mergeable, so a range's uniques is the merge of its buckets.
	VisitorsHLL []byte `json:"-"`

	// Bounded top-K count maps for the categorical breakdowns.
	TopPaths      map[string]int64 `gorm:"serializer:json" json:"-"`
	TopReferrers  map[string]int64 `gorm:"serializer:json" json:"-"`
	TopCountries  map[string]int64 `gorm:"serializer:json" json:"-"`
	TopUAFamilies map[string]int64 `gorm:"serializer:json" json:"-"` // browser family
	TopUserAgents map[string]int64 `gorm:"serializer:json" json:"-"` // raw user-agent string
	TopOS         map[string]int64 `gorm:"serializer:json" json:"-"`
	TopDevice     map[string]int64 `gorm:"serializer:json" json:"-"` // desktop | mobile | tablet | bot
	TopMethods    map[string]int64 `gorm:"serializer:json" json:"-"`

	BotRequests int64 `json:"-"`
}
