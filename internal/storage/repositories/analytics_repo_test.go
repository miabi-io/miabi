// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/analytics"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newAnalyticsDB(t *testing.T) *AnalyticsRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.AnalyticsRollup{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewAnalyticsRepository(db)
}

// rollupFrom aggregates a couple of events into a single flushed rollup.
func rollupFrom(bucket time.Time, ws, app uint, events ...*analytics.Event) *models.AnalyticsRollup {
	a := analytics.NewAggregator()
	for _, e := range events {
		e.Ts = bucket.UnixMilli()
		a.Ingest(e, ws, app)
	}
	rows := a.Flush(bucket.Add(2 * time.Minute))
	return rows[0]
}

func TestAnalyticsUpsertMergesAndQueries(t *testing.T) {
	repo := newAnalyticsDB(t)
	bucket := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)

	ev := func(status int, dur int64, vid string) *analytics.Event {
		return &analytics.Event{
			Route: "mb-ws5-api", Method: "GET", Status: status, Path: "/x", PathTemplate: "/x",
			ReqBytes: 10, RespBytes: 100, DurationMs: dur, UpstreamMs: dur, VID: vid,
			Country: "US", UA: "Mozilla/5.0 Chrome/120", RefererHost: "google.com",
		}
	}

	// First flush of the bucket.
	if err := repo.Upsert([]*models.AnalyticsRollup{
		rollupFrom(bucket, 5, 7, ev(200, 30, "a"), ev(500, 40, "b")),
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Second flush of the SAME bucket (e.g. late events / re-delivery) must merge.
	if err := repo.Upsert([]*models.AnalyticsRollup{
		rollupFrom(bucket, 5, 7, ev(200, 30, "a"), ev(404, 20, "c")),
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	rows, err := repo.Range(5, nil, bucket.Add(-time.Hour), bucket.Add(time.Hour))
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 merged row, got %d", len(rows))
	}
	r := rows[0]
	if r.Requests != 4 || r.Status2xx != 2 || r.Status4xx != 1 || r.Status5xx != 1 {
		t.Fatalf("merged counters wrong: %+v", r)
	}
	if u := analytics.UniquesOf(r.VisitorsHLL); u != 3 { // a,b,c
		t.Fatalf("merged uniques = %d, want 3", u)
	}

	// Report over the range.
	rep := analytics.BuildReport(rows, bucket.Add(-time.Hour), bucket.Add(time.Hour))
	if rep.Totals.Requests != 4 || rep.Totals.UniqueVisit != 3 {
		t.Fatalf("report totals wrong: %+v", rep.Totals)
	}
	if rep.Status.S5xx != 1 {
		t.Fatalf("report status wrong: %+v", rep.Status)
	}

	// App filter + distinct app listing.
	other := uint(7)
	got, err := repo.Range(5, &other, bucket.Add(-time.Hour), bucket.Add(time.Hour))
	if err != nil || len(got) != 1 {
		t.Fatalf("app-filtered range: rows=%d err=%v", len(got), err)
	}
	ids, err := repo.AppIDs(5, bucket.Add(-time.Hour), bucket.Add(time.Hour))
	if err != nil || len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("AppIDs = %v err=%v", ids, err)
	}

	// Prune removes it.
	n, err := repo.Prune(bucket.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune = %d err=%v", n, err)
	}
}

// RangeSummary must return the same counters as Range while leaving the top-K
// blobs undecoded. A wrong column name here would surface as silently zeroed
// traffic on the dashboard rather than as an error.
func TestRangeSummarySelectsCountersOnly(t *testing.T) {
	repo := newAnalyticsDB(t)
	bucket := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)

	ev := func(status int, dur int64, vid, path string) *analytics.Event {
		return &analytics.Event{
			Route: "mb-ws5-api", Method: "GET", Status: status, Path: path,
			ReqBytes: 10, RespBytes: 100, DurationMs: dur, UpstreamMs: dur, VID: vid,
			Country: "US", UA: "Mozilla/5.0 Chrome/120", RefererHost: "google.com",
		}
	}
	if err := repo.Upsert([]*models.AnalyticsRollup{
		rollupFrom(bucket, 5, 7, ev(200, 30, "a", "/"), ev(404, 20, "b", "/x"), ev(500, 40, "c", "/y")),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	from, to := bucket.Add(-time.Hour), bucket.Add(time.Hour)
	full, err := repo.Range(5, nil, from, to)
	if err != nil || len(full) != 1 {
		t.Fatalf("Range: rows=%d err=%v", len(full), err)
	}
	lean, err := repo.RangeSummary(5, nil, from, to, false)
	if err != nil || len(lean) != 1 {
		t.Fatalf("RangeSummary: rows=%d err=%v", len(lean), err)
	}

	f, l := full[0], lean[0]
	if !l.Bucket.Equal(f.Bucket) {
		t.Fatalf("bucket = %v, want %v", l.Bucket, f.Bucket)
	}
	if l.Requests != f.Requests || l.BytesIn != f.BytesIn || l.BytesOut != f.BytesOut {
		t.Fatalf("counters differ: lean %+v full %+v", l, f)
	}
	// The status columns are the easy ones to misname (status2xx, not status_2xx).
	if l.Status2xx != f.Status2xx || l.Status3xx != f.Status3xx ||
		l.Status4xx != f.Status4xx || l.Status5xx != f.Status5xx {
		t.Fatalf("status columns not selected: lean %d/%d/%d/%d, full %d/%d/%d/%d",
			l.Status2xx, l.Status3xx, l.Status4xx, l.Status5xx,
			f.Status2xx, f.Status3xx, f.Status4xx, f.Status5xx)
	}
	if l.DurationSum != f.DurationSum || len(l.DurationHist) != len(f.DurationHist) {
		t.Fatalf("latency columns not selected: lean sum=%d hist=%d", l.DurationSum, len(l.DurationHist))
	}
	if analytics.UniquesOf(l.VisitorsHLL) != analytics.UniquesOf(f.VisitorsHLL) {
		t.Fatalf("visitor sketch not selected: lean %d, full %d",
			analytics.UniquesOf(l.VisitorsHLL), analytics.UniquesOf(f.VisitorsHLL))
	}

	// The point of the narrower query: the top-K blobs are never read or decoded.
	if len(f.TopPaths) == 0 || len(f.TopCountries) == 0 {
		t.Fatal("fixture has no top paths or countries, so the assertions below prove nothing")
	}
	if len(l.TopPaths) != 0 || len(l.TopCountries) != 0 || len(l.TopMethods) != 0 || len(l.UpstreamHist) != 0 {
		t.Fatalf("summary decoded blobs it doesn't need: paths=%d countries=%d methods=%d upstream=%d",
			len(l.TopPaths), len(l.TopCountries), len(l.TopMethods), len(l.UpstreamHist))
	}

	// withGeo adds exactly one blob — the dashboard's country panel — and no more.
	geo, err := repo.RangeSummary(5, nil, from, to, true)
	if err != nil || len(geo) != 1 {
		t.Fatalf("RangeSummary(withGeo): rows=%d err=%v", len(geo), err)
	}
	g := geo[0]
	if g.Requests != f.Requests {
		t.Fatalf("withGeo counters differ: %d, want %d", g.Requests, f.Requests)
	}
	if g.TopCountries["US"] != f.TopCountries["US"] {
		t.Fatalf("country top-K not selected: %v, want %v", g.TopCountries, f.TopCountries)
	}
	if len(g.TopPaths) != 0 || len(g.TopMethods) != 0 || len(g.UpstreamHist) != 0 {
		t.Fatalf("withGeo decoded blobs beyond countries: paths=%d methods=%d upstream=%d",
			len(g.TopPaths), len(g.TopMethods), len(g.UpstreamHist))
	}
}
