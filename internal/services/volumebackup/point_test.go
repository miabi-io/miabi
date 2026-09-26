// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package volumebackup

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/dbenvelope"
	"github.com/miabi-io/miabi/internal/models"
)

func point(id uint, status models.BackupStatus, age time.Duration, pinned bool, now time.Time) models.VolumeBackup {
	return models.VolumeBackup{ID: id, Ref: "mbvol_x", Status: status, Pinned: pinned, CreatedAt: now.Add(-age)}
}

func prunedIDs(points []models.VolumeBackup, maxPoints, days int, now time.Time) []uint {
	var out []uint
	for _, i := range prunable(points, maxPoints, days, now) {
		out = append(out, points[i].ID)
	}
	return out
}

func TestPrunableKeepsMaxPoints(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	pts := []models.VolumeBackup{
		point(5, models.BackupCompleted, 0, false, now),
		point(4, models.BackupCompleted, day, false, now),
		point(3, models.BackupCompleted, 2*day, false, now),
		point(2, models.BackupCompleted, 3*day, false, now),
	}
	if got := prunedIDs(pts, 2, 0, now); !slices.Equal(got, []uint{3, 2}) {
		t.Fatalf("pruned %v, want [3 2]", got)
	}
}

// A pinned point survives and does not take a slot, so pinning one does not push a newer
// unpinned point out.
func TestPrunablePinnedTakesNoSlot(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	pts := []models.VolumeBackup{
		point(5, models.BackupCompleted, 0, true, now),
		point(4, models.BackupCompleted, day, false, now),
		point(3, models.BackupCompleted, 2*day, false, now),
	}
	if got := prunedIDs(pts, 1, 0, now); !slices.Equal(got, []uint{3}) {
		t.Fatalf("pruned %v, want [3]", got)
	}
}

// The newest completed point survives any policy, even when newer runs failed and the
// retention window has passed it.
func TestPrunableNeverRemovesLastGoodPoint(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	pts := []models.VolumeBackup{
		point(6, models.BackupFailed, 0, false, now),
		point(5, models.BackupFailed, day, false, now),
		point(4, models.BackupCompleted, 40*day, false, now),
		point(3, models.BackupCompleted, 41*day, false, now),
	}
	got := prunedIDs(pts, 1, 30, now)
	if slices.Contains(got, 4) {
		t.Fatalf("pruned the last completed point: %v", got)
	}
	if !slices.Equal(got, []uint{5, 3}) {
		t.Fatalf("pruned %v, want [5 3]", got)
	}
}

func TestPrunableLeavesRunsInFlight(t *testing.T) {
	now := time.Now()
	pts := []models.VolumeBackup{
		point(3, models.BackupCompleted, 0, false, now),
		point(2, models.BackupRunning, 50*24*time.Hour, false, now),
		point(1, models.BackupPending, 50*24*time.Hour, false, now),
	}
	if got := prunedIDs(pts, 1, 7, now); len(got) != 0 {
		t.Fatalf("pruned %v, want nothing while runs are in flight", got)
	}
}

func TestPrunableNoPolicyPrunesNothing(t *testing.T) {
	now := time.Now()
	pts := []models.VolumeBackup{point(1, models.BackupCompleted, 400*24*time.Hour, false, now), point(0, models.BackupCompleted, 500*24*time.Hour, false, now)}
	if got := prunedIDs(pts, 0, 0, now); len(got) != 0 {
		t.Fatalf("pruned %v with no policy", got)
	}
}

func TestCheckPoint(t *testing.T) {
	sealed, err := dbenvelope.Seal("data-key", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		b       models.VolumeBackup
		found   bool
		size    int64
		pass    string
		ok      bool
		errPart string
	}{
		{name: "present, right size, cleartext", b: models.VolumeBackup{Filename: "a.tar.gz", SizeBytes: 10}, found: true, size: 10, ok: true},
		{name: "missing", b: models.VolumeBackup{Filename: "a.tar.gz", SizeBytes: 10}, errPart: "missing"},
		{name: "resized", b: models.VolumeBackup{Filename: "a.tar.gz", SizeBytes: 10}, found: true, size: 9, errPart: "not the 10"},
		{name: "unknown recorded size is not a mismatch", b: models.VolumeBackup{Filename: "a.tar.gz"}, found: true, size: 9, ok: true},
		{name: "sealed and the passphrase opens it", b: models.VolumeBackup{Filename: "a.tar.gz.gpg", Envelope: sealed}, found: true, pass: "correct horse battery", ok: true},
		{name: "sealed with a rotated-away passphrase", b: models.VolumeBackup{Filename: "a.tar.gz.gpg", Envelope: sealed}, found: true, pass: "another one", errPart: "no longer opens"},
		{name: "sealed and no passphrase", b: models.VolumeBackup{Filename: "a.tar.gz.gpg", Envelope: sealed}, found: true, errPart: "no workspace backup passphrase"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := checkPoint(&tc.b, tc.found, tc.size, tc.pass)
			if res.OK != tc.ok {
				t.Fatalf("OK = %v (%s), want %v", res.OK, res.Error, tc.ok)
			}
			if tc.errPart != "" && !strings.Contains(res.Error, tc.errPart) {
				t.Errorf("error %q does not mention %q", res.Error, tc.errPart)
			}
		})
	}
}

func TestPointPrefix(t *testing.T) {
	if got := PointPrefix("/volumes/", "data", "mbvol_data_20260921T030000Z"); got != "volumes/data/mbvol_data_20260921T030000Z" {
		t.Errorf("PointPrefix = %q", got)
	}
	if got := PointPrefix("", "data", "r"); got != "data/r" {
		t.Errorf("PointPrefix with no base = %q", got)
	}
}

func TestNewVolumeBackupRef(t *testing.T) {
	at := time.Date(2026, 9, 21, 3, 0, 0, 0, time.FixedZone("x", 2*3600))
	if got := models.NewVolumeBackupRef("data", at); got != "mbvol_data_20260921T010000Z" {
		t.Errorf("ref = %q", got)
	}
}
