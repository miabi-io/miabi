// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package analytics

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func TestWorkspaceIDFromRoute(t *testing.T) {
	cases := map[string]uint{
		"mb-ws42-blog":     42,
		"mb-ws1-a":         1,
		"mb-ws007-x-y-z":   7,
		"mb-ws-blog":       0, // empty id
		"mb-ws42":          0, // no slug separator
		"platform-gateway": 0,
		"":                 0,
		"mb-wsxx-blog":     0, // non-numeric
	}
	for in, want := range cases {
		if got := WorkspaceIDFromRoute(in); got != want {
			t.Errorf("WorkspaceIDFromRoute(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestBucketIndexAndPercentile(t *testing.T) {
	// Every value <=5 in the first bucket, huge value in overflow.
	if bucketIndex(1) != 0 {
		t.Fatalf("bucketIndex(1) = %d", bucketIndex(1))
	}
	if got := bucketIndex(1 << 30); got != len(LatencyBoundsMs) {
		t.Fatalf("overflow bucketIndex = %d, want %d", got, len(LatencyBoundsMs))
	}

	// 1000 requests all at ~40ms → p50/p95/p99 land in the 50ms bucket.
	hist := make([]int64, histLen())
	hist[bucketIndex(40)] = 1000
	for _, p := range []float64{0.5, 0.95, 0.99} {
		if got := Percentile(hist, p); got != 50 {
			t.Errorf("Percentile(%.2f) = %v, want 50", p, got)
		}
	}
	if got := Percentile(make([]int64, histLen()), 0.5); got != 0 {
		t.Errorf("empty Percentile = %v, want 0", got)
	}
}

func TestTopKBounded(t *testing.T) {
	m := map[string]int64{}
	// Insert cap+50 distinct low-count keys, then a hot key.
	for i := 0; i < topKCap+50; i++ {
		topKAdd(m, "k"+strconv.Itoa(i), 1)
	}
	if len(m) > topKCap {
		t.Fatalf("map grew past cap: %d", len(m))
	}
	topKAdd(m, "hot", 999)
	if m["hot"] != 999 {
		t.Fatalf("hot key not retained: %d", m["hot"])
	}
	if len(m) > topKCap {
		t.Fatalf("map grew past cap after hot insert: %d", len(m))
	}
}

func TestClassifyUA(t *testing.T) {
	chrome := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36"
	fam, os, dev, bot := classifyUA(chrome)
	if fam != "Chrome" || os != "Windows" || dev != "desktop" || bot {
		t.Errorf("chrome: %q %q %q bot=%v", fam, os, dev, bot)
	}
	iphone := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605 Mobile/15E148 Safari/604"
	fam, os, dev, _ = classifyUA(iphone)
	if fam != "Safari" || os != "iOS" || dev != "mobile" {
		t.Errorf("iphone: %q %q %q", fam, os, dev)
	}
	if _, _, _, bot := classifyUA("Googlebot/2.1 (+http://www.google.com/bot.html)"); !bot {
		t.Error("googlebot not detected")
	}
}

func TestAggregatorIngestFlushAndMerge(t *testing.T) {
	a := NewAggregator()
	base := time.Date(2026, 7, 18, 10, 30, 15, 0, time.UTC).UnixMilli()

	mk := func(status int, dur int64, vid, path string) *Event {
		return &Event{
			// No PathTemplate: the gateway has no per-request patterns, so real
			// events carry only the request path.
			Ts: base, Route: "mb-ws9-shop", Method: "GET", Status: status,
			Path: path, ReqBytes: 100, RespBytes: 2000,
			DurationMs: dur, UpstreamMs: dur - 2, VID: vid, Country: "US",
			UA: "Mozilla/5.0 Chrome/120", RefererHost: "google.com",
		}
	}
	a.Ingest(mk(200, 40, "v1", "/"), 9, 3)
	a.Ingest(mk(200, 45, "v1", "/"), 9, 3) // same visitor
	a.Ingest(mk(404, 12, "v2", "/missing"), 9, 3)
	a.Ingest(mk(500, 900, "v3", "/checkout"), 9, 3)

	if a.Pending() != 1 {
		t.Fatalf("expected 1 open bucket, got %d", a.Pending())
	}

	// Nothing flushes while the minute is still "open".
	if got := a.Flush(time.UnixMilli(base).UTC().Truncate(time.Minute)); len(got) != 0 {
		t.Fatalf("flushed an open bucket: %d", len(got))
	}
	rows := a.Flush(time.UnixMilli(base).UTC().Add(2 * time.Minute))
	if len(rows) != 1 {
		t.Fatalf("expected 1 flushed row, got %d", len(rows))
	}
	r := rows[0]
	if r.WorkspaceID != 9 || r.ApplicationID != 3 || r.RouteName != "mb-ws9-shop" {
		t.Fatalf("row key wrong: %+v", r)
	}
	if r.Requests != 4 || r.Status2xx != 2 || r.Status4xx != 1 || r.Status5xx != 1 {
		t.Fatalf("counters wrong: req=%d 2xx=%d 4xx=%d 5xx=%d", r.Requests, r.Status2xx, r.Status4xx, r.Status5xx)
	}
	if r.BytesIn != 400 || r.BytesOut != 8000 {
		t.Fatalf("bytes wrong: in=%d out=%d", r.BytesIn, r.BytesOut)
	}
	if u := UniquesOf(r.VisitorsHLL); u != 3 {
		t.Fatalf("uniques = %d, want 3", u)
	}
	if r.TopCountries["US"] != 4 || r.TopMethods["GET"] != 4 || r.TopUAFamilies["Chrome"] != 4 {
		t.Fatalf("topK wrong: %+v %+v %+v", r.TopCountries, r.TopMethods, r.TopUAFamilies)
	}
	if r.TopOS["Other"] != 4 || r.TopDevice["desktop"] != 4 || r.BotRequests != 0 {
		t.Fatalf("ua enrichment wrong: os=%+v device=%+v bots=%d", r.TopOS, r.TopDevice, r.BotRequests)
	}

	// Merge a second, overlapping row (same visitors + a new one).
	b := NewAggregator()
	b.Ingest(mk(200, 40, "v1", "/"), 9, 3) // v1 again
	b.Ingest(mk(200, 40, "v9", "/"), 9, 3) // new visitor
	rows2 := b.Flush(time.UnixMilli(base).UTC().Add(2 * time.Minute))
	Merge(r, rows2[0])
	if r.Requests != 6 {
		t.Fatalf("merged requests = %d, want 6", r.Requests)
	}
	if u := UniquesOf(r.VisitorsHLL); u != 4 { // v1,v2,v3 + v9
		t.Fatalf("merged uniques = %d, want 4", u)
	}
	// Range-uniques via MergeUniques over both sketches.
	if u := MergeUniques([][]byte{rows[0].VisitorsHLL, rows2[0].VisitorsHLL}); u < 1 {
		t.Fatalf("MergeUniques returned %d", u)
	}
}

// The series must cover every bucket in the window, including the quiet ones:
// the charts plot it against a time axis, so a skipped bucket would silently
// shift every later column (and every axis label) to the wrong time.
func TestBuildReportFillsEmptyBuckets(t *testing.T) {
	until := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	since := until.Add(-24 * time.Hour)
	// Traffic in exactly two hours of the window, 5 hours apart.
	rows := []models.AnalyticsRollup{
		{Bucket: until.Add(-20 * time.Hour), Requests: 10, Status2xx: 8, Status4xx: 1, Status5xx: 1},
		{Bucket: until.Add(-15 * time.Hour), Requests: 4, Status2xx: 4},
	}
	rep := BuildReport(rows, since, until)

	if rep.Granularity != "hour" {
		t.Fatalf("granularity = %q, want hour", rep.Granularity)
	}
	if len(rep.Series) != 25 { // 24 whole hours, inclusive of both truncated ends
		t.Fatalf("series length = %d, want 25", len(rep.Series))
	}
	for i, p := range rep.Series {
		if want := since.Truncate(time.Hour).Add(time.Duration(i) * time.Hour); !p.T.Equal(want) {
			t.Fatalf("series[%d].T = %v, want %v", i, p.T, want)
		}
	}
	var nonEmpty, total int64
	for _, p := range rep.Series {
		if p.Requests > 0 {
			nonEmpty++
		}
		total += p.Requests
	}
	if nonEmpty != 2 || total != 14 {
		t.Fatalf("non-empty buckets = %d (want 2), total requests = %d (want 14)", nonEmpty, total)
	}
}

// Top paths is keyed on the request path, so distinct paths stay distinct. The gateway used to send its
// route mount prefix as PathTemplate — "/" for most routes — and since a template wins over the raw
// path, every request in a route collapsed into a single "/" row.
func TestIngestKeysPathsOnRequestPath(t *testing.T) {
	a := NewAggregator()
	base := time.Date(2026, 7, 18, 10, 30, 0, 0, time.UTC).UnixMilli()
	ev := func(path string) *Event {
		return &Event{Ts: base, Route: "mb-ws9-shop", Method: "GET", Status: 200, Path: path, VID: "v1"}
	}
	a.Ingest(ev("/"), 9, 3)
	a.Ingest(ev("/pricing"), 9, 3)
	a.Ingest(ev("/pricing"), 9, 3)
	a.Ingest(ev("/docs/getting-started"), 9, 3)
	// An over-long path is truncated rather than stored whole.
	long := "/" + strings.Repeat("x", 400)
	a.Ingest(ev(long), 9, 3)

	rows := a.Flush(time.UnixMilli(base).UTC().Add(2 * time.Minute))
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	paths := rows[0].TopPaths
	if paths["/pricing"] != 2 || paths["/"] != 1 || paths["/docs/getting-started"] != 1 {
		t.Fatalf("paths keyed wrong: %+v", paths)
	}
	if _, ok := paths[long]; ok {
		t.Fatalf("stored an unbounded path: %d bytes", len(long))
	}
	for k := range paths {
		if len(k) > maxPathLen+4 { // +4 covers the multi-byte ellipsis
			t.Fatalf("path key not clipped: %d bytes", len(k))
		}
	}
}

// A template still wins when the gateway can supply one, so the breakdown
// collapses /orders/1 and /orders/2 if routing ever grows real path patterns.
func TestIngestPrefersPathTemplateWhenPresent(t *testing.T) {
	a := NewAggregator()
	base := time.Date(2026, 7, 18, 10, 30, 0, 0, time.UTC).UnixMilli()
	for _, id := range []string{"1", "2", "3"} {
		a.Ingest(&Event{
			Ts: base, Route: "mb-ws9-shop", Method: "GET", Status: 200,
			Path: "/orders/" + id, PathTemplate: "/orders/:id", VID: "v1",
		}, 9, 3)
	}
	rows := a.Flush(time.UnixMilli(base).UTC().Add(2 * time.Minute))
	if got := rows[0].TopPaths["/orders/:id"]; got != 3 {
		t.Fatalf("template not preferred: %+v", rows[0].TopPaths)
	}
}

// The dashboard reads BuildSummary and the analytics pages read BuildReport, so
// the figures they share have to agree — otherwise the same workspace shows two
// different request counts on two pages.
func TestBuildSummaryMatchesReport(t *testing.T) {
	until := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	since := until.Add(-24 * time.Hour)

	mk := func(offset time.Duration, route string, reqs, s2, s4, s5 int64) models.AnalyticsRollup {
		a := NewAggregator()
		bucket := until.Add(-offset)
		for i := int64(0); i < reqs; i++ {
			status := 200
			switch {
			case i < s5:
				status = 500
			case i < s5+s4:
				status = 404
			}
			a.Ingest(&Event{
				Ts: bucket.UnixMilli(), Route: route, Method: "GET", Status: status,
				Path: "/p", DurationMs: 20 + i, UpstreamMs: 10, VID: "v" + strconv.FormatInt(i, 10),
				UA: "Mozilla/5.0 Chrome/120", Country: "US",
			}, 9, 3)
		}
		_ = s2
		return *a.Flush(bucket.Add(2 * time.Minute))[0]
	}

	rows := []models.AnalyticsRollup{
		mk(20*time.Hour, "mb-ws9-a", 10, 8, 1, 1),
		mk(15*time.Hour, "mb-ws9-b", 6, 6, 0, 0),
		mk(2*time.Hour, "mb-ws9-a", 4, 3, 1, 0),
	}

	rep := BuildReport(rows, since, until)
	sum := BuildSummary(rows, since, until)

	if sum.Granularity != rep.Granularity {
		t.Fatalf("granularity: summary %q, report %q", sum.Granularity, rep.Granularity)
	}
	if sum.Totals != rep.Totals {
		t.Fatalf("totals differ:\n summary %+v\n report  %+v", sum.Totals, rep.Totals)
	}
	if sum.Status != rep.Status {
		t.Fatalf("status differ: summary %+v, report %+v", sum.Status, rep.Status)
	}
	if len(sum.Series) != len(rep.Series) {
		t.Fatalf("series length: summary %d, report %d", len(sum.Series), len(rep.Series))
	}
	for i := range sum.Series {
		if sum.Series[i] != rep.Series[i] {
			t.Fatalf("series[%d] differs:\n summary %+v\n report  %+v", i, sum.Series[i], rep.Series[i])
		}
	}

	// The dashboard's country panel and the analytics page's country breakdown are
	// the same ranking, the dashboard's simply truncated — a workspace must not see
	// a different leader on two pages.
	if len(sum.TopCountries) > SummaryTopCountries {
		t.Fatalf("summary returned %d countries, want at most %d", len(sum.TopCountries), SummaryTopCountries)
	}
	if len(sum.TopCountries) == 0 {
		t.Fatal("fixture ingests a country, so the summary should rank one")
	}
	for i, c := range sum.TopCountries {
		if c != rep.Web.TopCountries[i] {
			t.Fatalf("countries[%d] differs: summary %+v, report %+v", i, c, rep.Web.TopCountries[i])
		}
	}
}
