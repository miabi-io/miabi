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

package analytics

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// RouteStats is one route's traffic over a short window, for the route's Flow tab.
type RouteStats struct {
	Enabled   bool        `json:"enabled"`
	Range     Window      `json:"range"`
	Requests  int64       `json:"requests"`
	PerMinute float64     `json:"per_minute"`
	Visitors  int64       `json:"visitors"`
	Status    StatusBreak `json:"status"`
	// Codes counts 3xx–5xx by exact code; rollups written before codes were kept count only by class.
	Codes map[string]int64 `json:"codes"`
	P95Ms float64          `json:"p95_ms"`
	AvgMs float64          `json:"avg_ms"`
	// AvgGatewayMs is a mean of (total − upstream) time: the two histograms cannot be subtracted into a percentile.
	AvgGatewayMs float64      `json:"avg_gateway_ms"`
	Series       []RoutePoint `json:"series"`
}

type RoutePoint struct {
	T        time.Time `json:"t"`
	Requests int64     `json:"requests"`
	Errors   int64     `json:"errors"`
}

// BuildRouteStats fills every minute of the window, so a quiet minute reads as zero rather than a gap.
func BuildRouteStats(rows []models.AnalyticsRollup, since, until time.Time) RouteStats {
	out := RouteStats{Enabled: true, Range: Window{Since: since, Until: until}, Codes: map[string]int64{}, Series: []RoutePoint{}}
	dur := make([]int64, histLen())
	var durSum, upSum int64
	var sketches [][]byte
	perMinute := map[int64]*RoutePoint{}
	for i := range rows {
		r := &rows[i]
		out.Requests += r.Requests
		out.Status.S2xx += r.Status2xx
		out.Status.S3xx += r.Status3xx
		out.Status.S4xx += r.Status4xx
		out.Status.S5xx += r.Status5xx
		for code, n := range r.StatusCodes {
			out.Codes[code] += n
		}
		dur = addHist(dur, r.DurationHist)
		durSum += r.DurationSum
		upSum += r.UpstreamSum
		if len(r.VisitorsHLL) > 0 {
			sketches = append(sketches, r.VisitorsHLL)
		}
		k := r.Bucket.UTC().Truncate(time.Minute).Unix()
		p := perMinute[k]
		if p == nil {
			p = &RoutePoint{T: time.Unix(k, 0).UTC()}
			perMinute[k] = p
		}
		p.Requests += r.Requests
		p.Errors += r.Status5xx
	}
	for t := since.UTC().Truncate(time.Minute); t.Before(until); t = t.Add(time.Minute) {
		if p := perMinute[t.Unix()]; p != nil {
			out.Series = append(out.Series, *p)
		} else {
			out.Series = append(out.Series, RoutePoint{T: t})
		}
	}
	if minutes := until.Sub(since).Minutes(); minutes > 0 {
		out.PerMinute = float64(out.Requests) / minutes
	}
	if out.Requests > 0 {
		out.AvgMs = float64(durSum) / float64(out.Requests)
		if gw := durSum - upSum; gw > 0 {
			out.AvgGatewayMs = float64(gw) / float64(out.Requests)
		}
	}
	out.P95Ms = Percentile(dur, 0.95)
	out.Visitors = MergeUniques(sketches)
	return out
}
