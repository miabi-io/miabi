// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newSetService(t *testing.T) (*Service, *repositories.DatabaseBackupSetRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.DatabaseBackupSet{}, &models.Backup{}); err != nil {
		t.Fatal(err)
	}
	sets := repositories.NewDatabaseBackupSetRepository(db)
	// clients is nil: every item below is an s3 backup, so Delete never reaches the
	// Docker branch that removes a local artifact.
	svc := NewService(repositories.NewBackupRepository(db), nil, nil)
	svc.SetSetRepository(sets)
	return svc, sets, db
}

// seedSet writes a set aged `age` old, with one item per name.
func seedSet(t *testing.T, db *gorm.DB, sets *repositories.DatabaseBackupSetRepository,
	ref string, status models.BackupStatus, age time.Duration, pinned bool) *models.DatabaseBackupSet {
	t.Helper()
	at := time.Now().Add(-age)
	set := &models.DatabaseBackupSet{
		WorkspaceID: 1, InstanceID: 7, Ref: ref, Status: status,
		Destination: "s3", CreatedAt: at,
	}
	if err := sets.Create(set); err != nil {
		t.Fatal(err)
	}
	item := &models.Backup{
		WorkspaceID: 1, DatabaseID: 1, SetID: &set.ID, Status: status,
		Destination: "s3", Pinned: pinned, CreatedAt: at,
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatal(err)
	}
	return set
}

func remainingRefs(t *testing.T, sets *repositories.DatabaseBackupSetRepository) []string {
	t.Helper()
	got, err := sets.ListByInstance(7)
	if err != nil {
		t.Fatal(err)
	}
	refs := make([]string, len(got))
	for i := range got {
		refs[i] = got[i].Ref
	}
	return refs
}

func TestPruneSetsKeepsTheMostRecent(t *testing.T) {
	svc, sets, db := newSetService(t)
	for i, ref := range []string{"old-1", "old-2", "old-3"} {
		seedSet(t, db, sets, ref, models.BackupCompleted, time.Duration(i+1)*time.Hour, false)
	}
	seedSet(t, db, sets, "newest", models.BackupCompleted, 0, false)

	removed, err := svc.PruneSets(context.Background(), 7, 2, 0)
	if err != nil {
		t.Fatalf("PruneSets: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	got := remainingRefs(t, sets)
	if len(got) != 2 || got[0] != "newest" || got[1] != "old-1" {
		t.Errorf("kept %q, want the two most recent", got)
	}
}

// The rule that turns a mistyped retention from data loss into a no-op.
func TestPruneSetsNeverRemovesTheLastSuccessfulSet(t *testing.T) {
	svc, sets, db := newSetService(t)
	seedSet(t, db, sets, "only", models.BackupCompleted, 400*24*time.Hour, false)

	removed, err := svc.PruneSets(context.Background(), 7, 0, 1)
	if err != nil {
		t.Fatalf("PruneSets: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 — the only successful set must survive", removed)
	}
	if got := remainingRefs(t, sets); len(got) != 1 {
		t.Errorf("remaining = %q, want the set kept", got)
	}
}

// A failed set is not a recovery point, so it does not protect anything: the
// newest *completed* set is what survives.
func TestPruneSetsKeepsTheNewestCompletedNotTheNewestSet(t *testing.T) {
	svc, sets, db := newSetService(t)
	seedSet(t, db, sets, "good", models.BackupCompleted, 48*time.Hour, false)
	seedSet(t, db, sets, "failed", models.BackupFailed, 36*time.Hour, false)

	// Both are past the cutoff. The completed one survives as the last good set;
	// the newer failed one does not, because it was never a recovery point.
	removed, err := svc.PruneSets(context.Background(), 7, 0, 1)
	if err != nil {
		t.Fatalf("PruneSets: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	got := remainingRefs(t, sets)
	if len(got) != 1 || got[0] != "good" {
		t.Errorf("kept %q, want only the newest completed set", got)
	}
}

func TestPruneSetsSkipsASetWithAPinnedMember(t *testing.T) {
	svc, sets, db := newSetService(t)
	seedSet(t, db, sets, "pinned", models.BackupCompleted, 72*time.Hour, true)
	seedSet(t, db, sets, "prunable", models.BackupCompleted, 48*time.Hour, false)
	seedSet(t, db, sets, "newest", models.BackupCompleted, 0, false)

	// Keep 1: without the pin exemption this would delete both older sets.
	removed, err := svc.PruneSets(context.Background(), 7, 1, 0)
	if err != nil {
		t.Fatalf("PruneSets: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	got := remainingRefs(t, sets)
	if len(got) != 2 || got[0] != "newest" || got[1] != "pinned" {
		t.Errorf("kept %q, want the newest plus the pinned set", got)
	}
}

func TestPruneSetsRemovesTheItemsWithTheSet(t *testing.T) {
	svc, sets, db := newSetService(t)
	seedSet(t, db, sets, "stale", models.BackupCompleted, 72*time.Hour, false)
	seedSet(t, db, sets, "newest", models.BackupCompleted, 0, false)

	if _, err := svc.PruneSets(context.Background(), 7, 1, 0); err != nil {
		t.Fatalf("PruneSets: %v", err)
	}
	var orphans int64
	if err := db.Model(&models.Backup{}).Where("set_id IS NOT NULL").Count(&orphans).Error; err != nil {
		t.Fatal(err)
	}
	if orphans != 1 {
		t.Errorf("%d set items remain, want 1 — a pruned set must take its items", orphans)
	}
}

func TestPruneSetsWithNoBoundsDoesNothing(t *testing.T) {
	svc, sets, db := newSetService(t)
	seedSet(t, db, sets, "a", models.BackupCompleted, 100*24*time.Hour, false)
	removed, err := svc.PruneSets(context.Background(), 7, 0, 0)
	if err != nil {
		t.Fatalf("PruneSets: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 when no policy is set", removed)
	}
}

// A partial recovery point is not a recovery point: one failed item fails the set,
// and the set says which database stopped it.
func TestFinishSetFailsOnAnyFailedItem(t *testing.T) {
	svc, sets, db := newSetService(t)
	set := seedSet(t, db, sets, "mixed", models.BackupRunning, 0, false)
	inst := &models.DatabaseInstance{ID: 7, Name: "pg", WorkspaceID: 1}

	items := []*models.Backup{
		{DatabaseID: 1, Status: models.BackupCompleted, SizeBytes: 100, Encrypted: true},
		{DatabaseID: 2, Status: models.BackupFailed, Error: "dump refused"},
		{DatabaseID: 3, Status: models.BackupCompleted, SizeBytes: 50, Encrypted: true},
	}
	got := svc.finishSet(set, inst, items)

	if got.Status != models.BackupFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error == "" {
		t.Error("a failed set carries no error")
	}
	if got.Encrypted {
		t.Error("a failed set must not be reported as encrypted")
	}
	if got.SizeBytes != 150 {
		t.Errorf("SizeBytes = %d, want the completed items summed", got.SizeBytes)
	}
}

func TestFinishSetEncryptedOnlyWhenEveryItemIs(t *testing.T) {
	inst := &models.DatabaseInstance{ID: 7, Name: "pg", WorkspaceID: 1}
	cases := []struct {
		name string
		enc  []bool
		want bool
	}{
		{"every artifact sealed", []bool{true, true}, true},
		{"one artifact in the clear", []bool{true, false}, false},
		{"none sealed", []bool{false, false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, sets, db := newSetService(t)
			set := seedSet(t, db, sets, "enc-"+tc.name, models.BackupRunning, 0, false)
			items := make([]*models.Backup, len(tc.enc))
			for i, e := range tc.enc {
				items[i] = &models.Backup{DatabaseID: uint(i + 1), Status: models.BackupCompleted, Encrypted: e}
			}
			if got := svc.finishSet(set, inst, items); got.Encrypted != tc.want {
				t.Errorf("Encrypted = %v, want %v", got.Encrypted, tc.want)
			}
		})
	}
}

// A recovery point exists to survive the loss of its host, so it must live in
// object storage. The local backup volume dies with the node it is on.
func TestRunSetRefusesWithoutS3(t *testing.T) {
	svc, _, _ := newSetService(t)
	inst := &models.DatabaseInstance{ID: 7, Name: "pg", WorkspaceID: 1, Engine: models.DBEnginePostgres}

	for _, dest := range []Destination{
		{Type: "local"},
		{Type: ""},
		{Type: "s3"}, // s3 named but no target configured
	} {
		if _, err := svc.RunSet(context.Background(), inst, SetOptions{Trigger: "manual"}, dest); !errors.Is(err, ErrS3Required) {
			t.Errorf("RunSet(%+v) error = %v, want ErrS3Required", dest, err)
		}
	}
}

// Retention counts sets, not the backups inside them: a set of eight databases is
// one recovery point, so "keep 2" keeps two sets and not two dumps.
func TestPruneSetsCountsSetsNotItems(t *testing.T) {
	svc, sets, db := newSetService(t)
	for i, ref := range []string{"newest", "middle", "oldest"} {
		set := seedSet(t, db, sets, ref, models.BackupCompleted, time.Duration(i)*time.Hour, false)
		// Three more databases in each set, so item counts cannot be what is counted.
		for n := 2; n <= 4; n++ {
			extra := &models.Backup{
				WorkspaceID: 1, DatabaseID: uint(n), SetID: &set.ID,
				Status: models.BackupCompleted, Destination: "s3", CreatedAt: set.CreatedAt,
			}
			if err := db.Create(extra).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	removed, err := svc.PruneSets(context.Background(), 7, 2, 0)
	if err != nil {
		t.Fatalf("PruneSets: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d sets, want 1", removed)
	}
	if got := remainingRefs(t, sets); len(got) != 2 {
		t.Errorf("kept %q, want 2 sets", got)
	}
}

func TestNewDatabaseBackupSetRef(t *testing.T) {
	at := time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC)
	if got, want := models.NewDatabaseBackupSetRef("pg-main", at), "mbdb_pg-main_20260909T030000Z"; got != want {
		t.Errorf("ref = %q, want %q", got, want)
	}
	if got := models.NewDatabaseBackupSetRef("", at); got != "mbdb_unknown_20260909T030000Z" {
		t.Errorf("ref with no instance name = %q", got)
	}
}
