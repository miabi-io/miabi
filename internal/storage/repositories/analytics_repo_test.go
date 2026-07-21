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
