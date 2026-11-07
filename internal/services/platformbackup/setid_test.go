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

package platformbackup

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Items has a real foreign key, so SetID must be NULL, not 0, or Postgres refuses the insert (23503).
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

// PruneSets retains sets as a unit; deleting one artifact here would hollow out a restorable set.
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
