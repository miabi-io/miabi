// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package analytics

import (
	"strconv"
	"testing"
	"time"
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
			Ts: base, Route: "mb-ws9-shop", Method: "GET", Status: status,
			Path: path, PathTemplate: path, ReqBytes: 100, RespBytes: 2000,
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
