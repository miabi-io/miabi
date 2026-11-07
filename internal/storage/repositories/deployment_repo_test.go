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

package repositories

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newDeploymentDB(t *testing.T) *DeploymentRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Deployment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewDeploymentRepository(db)
}

// AppendLog writes straight to the column while in-memory Logs is "", so a plain Save would wipe it.
func TestUpdatePreservesAppendedLog(t *testing.T) {
	repo := newDeploymentDB(t)
	dep := &models.Deployment{ApplicationID: 1}
	if err := repo.Create(dep); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The build streams its log straight into the DB column.
	for _, line := range []string{"building image", "pushing image", "pushed digest sha256:abc"} {
		if err := repo.AppendLog(dep.ID, line); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// The worker persists status/image with a struct whose Logs is still "".
	dep.Status = models.DeploymentSucceeded
	dep.Image = "reg/app@sha256:abc"
	if err := repo.Update(dep); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := repo.FindByID(dep.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !strings.Contains(got.Logs, "building image") || !strings.Contains(got.Logs, "pushed digest") {
		t.Errorf("Update wiped the appended log; got %q", got.Logs)
	}
	if got.Image != "reg/app@sha256:abc" || got.Status != models.DeploymentSucceeded {
		t.Errorf("Update didn't persist other fields: image=%q status=%q", got.Image, got.Status)
	}
}

func TestDeploymentNumberPerApp(t *testing.T) {
	repo := newDeploymentDB(t)

	// Interleave two apps so the global IDs and per-app numbers diverge.
	want := []struct {
		appID  uint
		number int
	}{
		{appID: 1, number: 1},
		{appID: 1, number: 2},
		{appID: 2, number: 1}, // a different app starts its own sequence at 1
		{appID: 1, number: 3},
		{appID: 2, number: 2},
	}
	for i, w := range want {
		dep := &models.Deployment{ApplicationID: w.appID}
		if err := repo.Create(dep); err != nil {
			t.Fatalf("create #%d: %v", i, err)
		}
		if dep.Number != w.number {
			t.Errorf("deploy %d (app %d): Number = %d, want %d", i, w.appID, dep.Number, w.number)
		}
		// The global ID is monotonic across all apps; the number is per-app.
		if dep.ID != uint(i+1) {
			t.Errorf("deploy %d: ID = %d, want %d", i, dep.ID, i+1)
		}
	}
}

func TestDeploymentSetLogMeta(t *testing.T) {
	repo := newDeploymentDB(t)
	dep := &models.Deployment{ApplicationID: 1, Logs: "line one\nline two\nline three\n"}
	if err := repo.Create(dep); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A zero ref (store disabled / failed) must leave the row untouched.
	if err := repo.SetLogMeta(dep.ID, "", "tail", 10, 1, false); err != nil {
		t.Fatalf("SetLogMeta noop: %v", err)
	}
	got, _ := repo.FindByID(dep.ID)
	if got.Logs != "line one\nline two\nline three\n" || got.LogRef != "" {
		t.Fatalf("zero ref should be a no-op, got logs=%q ref=%q", got.Logs, got.LogRef)
	}

	// A real externalization trims the column to the tail and records metadata.
	ref := "logs/deployment/ws_1/app-1/dep-1.log"
	if err := repo.SetLogMeta(dep.ID, ref, "line three\n", 29, 3, true); err != nil {
		t.Fatalf("SetLogMeta: %v", err)
	}
	got, _ = repo.FindByID(dep.ID)
	if got.LogRef != ref {
		t.Errorf("LogRef = %q, want %q", got.LogRef, ref)
	}
	if got.Logs != "line three\n" {
		t.Errorf("Logs = %q, want bounded tail", got.Logs)
	}
	if got.LogBytes != 29 || got.LogLines != 3 || !got.LogTruncated {
		t.Errorf("counters = (%d, %d, %v), want (29, 3, true)", got.LogBytes, got.LogLines, got.LogTruncated)
	}
}

func TestDeploymentNumberExplicitPreserved(t *testing.T) {
	repo := newDeploymentDB(t)
	dep := &models.Deployment{ApplicationID: 1, Number: 42}
	if err := repo.Create(dep); err != nil {
		t.Fatalf("create: %v", err)
	}
	if dep.Number != 42 {
		t.Errorf("Number = %d, want 42 (explicit value preserved)", dep.Number)
	}
}
