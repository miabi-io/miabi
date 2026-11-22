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
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func TestAggregatorCountsErrorCodes(t *testing.T) {
	a := NewAggregator()
	ts := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC).UnixMilli()
	for _, st := range []int{200, 401, 401, 429, 502, 302} {
		a.Ingest(&Event{Ts: ts, Route: "mb-ws1-api", Status: st, DurationMs: 10}, 1, 2)
	}
	rows := a.Flush(time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC))
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	got := rows[0].StatusCodes
	if got["401"] != 2 || got["429"] != 1 || got["502"] != 1 || got["302"] != 1 || len(got) != 4 {
		t.Errorf("codes = %v", got)
	}
	dst := &models.AnalyticsRollup{}
	Merge(dst, rows[0])
	Merge(dst, rows[0])
	if dst.StatusCodes["401"] != 4 {
		t.Errorf("merged 401 = %d", dst.StatusCodes["401"])
	}
}

func TestBuildRouteStats(t *testing.T) {
	until := time.Date(2026, 11, 22, 10, 15, 0, 0, time.UTC)
	since := until.Add(-15 * time.Minute)
	hist := make([]int64, histLen())
	hist[bucketIndex(40)] = 10
	rows := []models.AnalyticsRollup{
		{Bucket: since.Add(2 * time.Minute), Requests: 10, Status2xx: 7, Status4xx: 2, Status5xx: 1,
			DurationHist: hist, DurationSum: 400, UpstreamSum: 350, StatusCodes: map[string]int64{"401": 2, "503": 1}},
		{Bucket: since.Add(2 * time.Minute), Requests: 5, Status2xx: 5, DurationSum: 50, UpstreamSum: 40},
	}
	s := BuildRouteStats(rows, since, until)
	if s.Requests != 15 || s.PerMinute != 1 || s.Status.S4xx != 2 || s.Codes["401"] != 2 || s.Codes["503"] != 1 {
		t.Errorf("stats = %+v", s)
	}
	if s.AvgMs != 30 || s.AvgGatewayMs != 4 {
		t.Errorf("avg = %v, gateway = %v", s.AvgMs, s.AvgGatewayMs)
	}
	if len(s.Series) != 15 || s.Series[2].Requests != 15 || s.Series[2].Errors != 1 || s.Series[0].Requests != 0 {
		t.Errorf("series = %+v", s.Series)
	}
	if s.P95Ms <= 0 {
		t.Errorf("p95 = %v", s.P95Ms)
	}
}

func TestBuildRouteStatsVariants(t *testing.T) {
	until := time.Date(2026, 11, 22, 10, 15, 0, 0, time.UTC)
	since := until.Add(-15 * time.Minute)
	rows := []models.AnalyticsRollup{
		{Bucket: since, Variant: "stable", Requests: 90, Status5xx: 0},
		{Bucket: since, Variant: "canary", Requests: 10, Status5xx: 2},
		{Bucket: since, Requests: 50},
	}
	s := BuildRouteStats(rows, since, until)
	st, cn := s.Variants["stable"], s.Variants["canary"]
	if st == nil || cn == nil || s.Requests != 150 {
		t.Fatalf("variants = %+v", s.Variants)
	}
	if st.Share != 0.9 || cn.Share != 0.1 || cn.ErrorRate != 0.2 || st.ErrorRate != 0 {
		t.Errorf("stable = %+v, canary = %+v", st, cn)
	}

	if s := BuildRouteStats(rows[2:], since, until); s.Variants != nil {
		t.Errorf("no named backend should give no variants: %+v", s.Variants)
	}
}

func TestVariantOf(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "http://mb-app-x1-89:8080": "stable", "http://mb-app-x1-89-canary:8080": "canary",
		"https://mb-app-7-canary": "canary", "mb-app-7": "stable",
	} {
		if got := VariantOf(in); got != want {
			t.Errorf("VariantOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAggregatorKeepsVariantsApart(t *testing.T) {
	a := NewAggregator()
	ts := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC).UnixMilli()
	a.Ingest(&Event{Ts: ts, Route: "mb-ws1-api", Status: 200, Backend: "http://mb-app-1:80"}, 1, 1)
	a.Ingest(&Event{Ts: ts, Route: "mb-ws1-api", Status: 500, Backend: "http://mb-app-1-canary:80"}, 1, 1)
	a.Ingest(&Event{Ts: ts, Route: "mb-ws1-api", Status: 200}, 1, 1)
	rows := a.Flush(time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC))
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Variant] += r.Requests
	}
	if len(rows) != 3 || got["stable"] != 1 || got["canary"] != 1 || got[""] != 1 {
		t.Errorf("rows by variant = %v", got)
	}
}

func TestCompareVariants(t *testing.T) {
	rows := func(cr, c5, sr, s5 int64) []models.AnalyticsRollup {
		return []models.AnalyticsRollup{
			{Variant: VariantCanary, Requests: cr, Status5xx: c5},
			{Variant: VariantStable, Requests: sr, Status5xx: s5},
			{Requests: 1000, Status5xx: 1000},
		}
	}
	if c := CompareVariants(rows(100, 3, 900, 1)); !c.Regressed {
		t.Errorf("3%% vs 0.1%% should regress: %+v", c)
	}
	if c := CompareVariants(rows(40, 20, 900, 0)); c.Regressed {
		t.Errorf("too few canary requests: %+v", c)
	}
	if c := CompareVariants(rows(100, 1, 900, 1)); c.Regressed {
		t.Errorf("under a point apart: %+v", c)
	}
}
