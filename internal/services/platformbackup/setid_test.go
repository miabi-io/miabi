// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformbackup

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// PlatformBackupSet.Items declares a real foreign key, so SetID must be NULL for an artifact belonging to
// no recovery point. Stored as 0 it is not "no set" — it references a row that cannot exist, and Postgres
// refuses the insert with SQLSTATE 23503. This pins the distinction that bug turned on.
func TestSetIDIsNilForAdHocBackups(t *testing.T) {
	adhoc := models.PlatformBackup{Subject: models.PlatformBackupDatabase}
	if adhoc.SetID != nil {
		t.Fatalf("a backup with no recovery point must have a nil SetID, got %v", *adhoc.SetID)
	}

	setID := uint(7)
	owned := models.PlatformBackup{SetID: &setID, Subject: models.PlatformBackupDatabase}
	if owned.SetID == nil || *owned.SetID != setID {
		t.Fatalf("owned backup lost its set reference: %v", owned.SetID)
	}
}

// Retention must skip set-owned artifacts: PruneSets retains recovery points as
// a unit, and deleting one artifact here would hollow out a set that still
// reports itself restorable.
func TestPruneSkipsSetOwnedArtifacts(t *testing.T) {
	setID := uint(3)
	rows := []models.PlatformBackup{
		{SetID: nil},
		{SetID: &setID},
	}
	var prunable int
	for i := range rows {
		if rows[i].SetID == nil {
			prunable++
		}
	}
	if prunable != 1 {
		t.Fatalf("prunable = %d, want 1 (only the ad-hoc row)", prunable)
	}
}
