// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newPruneService builds a service over sqlite. Every fixture uses the s3
// destination so Delete never reaches for a docker client.
func newPruneService(t *testing.T) (*Service, *repositories.BackupRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Backup{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := repositories.NewBackupRepository(db)
	return NewService(repo, nil, nil), repo
}

// seedBackups writes len(pinned) backups oldest-first, so index 0 is the oldest.
func seedBackups(t *testing.T, repo *repositories.BackupRepository, pinned []bool, ages []time.Duration) []models.Backup {
	t.Helper()
	now := time.Now()
	out := make([]models.Backup, 0, len(pinned))
	for i := range pinned {
		b := &models.Backup{
			WorkspaceID: 1, DatabaseID: 1, Engine: models.DBEnginePostgres,
			Status: models.BackupCompleted, Destination: "s3", Pinned: pinned[i],
			CreatedAt: now.Add(-ages[i]),
		}
		if err := repo.Create(b); err != nil {
			t.Fatalf("create backup: %v", err)
		}
		out = append(out, *b)
	}
	return out
}

func remaining(t *testing.T, repo *repositories.BackupRepository) []models.Backup {
	t.Helper()
	rows, err := repo.ListByDatabase(1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return rows
}

func TestPruneKeepsPinnedBackupsOverCount(t *testing.T) {
	svc, repo := newPruneService(t)
	// Oldest first: the oldest is pinned and would otherwise be the first to go.
	ages := []time.Duration{5 * time.Hour, 4 * time.Hour, 3 * time.Hour, 2 * time.Hour, time.Hour}
	seedBackups(t, repo, []bool{true, false, false, false, false}, ages)

	removed, err := svc.Prune(context.Background(), 1, 2, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	rows := remaining(t, repo)
	// 4 unpinned, keep the 2 newest of them → 2 removed; the pin survives.
	if removed != 2 {
		t.Fatalf("removed %d, want 2", removed)
	}
	if len(rows) != 3 {
		t.Fatalf("%d backups left, want 3", len(rows))
	}
	var pinned int
	for _, b := range rows {
		if b.Pinned {
			pinned++
		}
	}
	if pinned != 1 {
		t.Fatal("the pinned backup was pruned")
	}
}

// A pin must not silently shrink the rolling window: "keep last 2" still keeps two
// unpinned backups, rather than counting the pin as one of them.
func TestPruneDoesNotLetAPinConsumeARetentionSlot(t *testing.T) {
	svc, repo := newPruneService(t)
	ages := []time.Duration{4 * time.Hour, 3 * time.Hour, 2 * time.Hour, time.Hour}
	seedBackups(t, repo, []bool{false, true, false, false}, ages)

	if _, err := svc.Prune(context.Background(), 1, 2, 0); err != nil {
		t.Fatalf("prune: %v", err)
	}
	rows := remaining(t, repo)
	unpinned := 0
	for _, b := range rows {
		if !b.Pinned {
			unpinned++
		}
	}
	if unpinned != 2 {
		t.Fatalf("%d unpinned backups left, want 2", unpinned)
	}
	if len(rows) != 3 {
		t.Fatalf("%d backups left, want 3 (two unpinned plus the pin)", len(rows))
	}
}

func TestPruneKeepsPinnedBackupsPastTheAgeLimit(t *testing.T) {
	svc, repo := newPruneService(t)
	ages := []time.Duration{40 * 24 * time.Hour, 39 * 24 * time.Hour, time.Hour}
	seedBackups(t, repo, []bool{true, false, false}, ages)

	removed, err := svc.Prune(context.Background(), 1, 0, 30)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed %d, want 1 — only the unpinned stale backup", removed)
	}
	rows := remaining(t, repo)
	if len(rows) != 2 {
		t.Fatalf("%d backups left, want 2", len(rows))
	}
}

func TestAnnotateLeavesUnsetFieldsAlone(t *testing.T) {
	svc, repo := newPruneService(t)
	rows := seedBackups(t, repo, []bool{false}, []time.Duration{time.Hour})
	b := &rows[0]

	note := "before the v1.3.0 rollout"
	if err := svc.Annotate(b, &note, nil); err != nil {
		t.Fatalf("annotate comment: %v", err)
	}
	pin := true
	if err := svc.Annotate(b, nil, &pin); err != nil {
		t.Fatalf("annotate pin: %v", err)
	}

	got := remaining(t, repo)[0]
	if got.Comment != note {
		t.Fatalf("comment = %q, want %q", got.Comment, note)
	}
	if !got.Pinned {
		t.Fatal("pinning the backup did not stick")
	}
}
