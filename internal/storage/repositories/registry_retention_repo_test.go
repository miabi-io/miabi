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
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func retentionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Release{}, &models.RegistryRetention{}, &models.RegistryRetentionOverride{}, &models.RegistryRetentionRun{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestListRecentPerApp(t *testing.T) {
	db := retentionDB(t)
	for app := uint(1); app <= 2; app++ {
		for v := 1; v <= 4; v++ {
			db.Create(&models.Release{ApplicationID: app, Version: v, Image: "img", DeploymentID: 1})
		}
	}
	rels, err := NewReleaseRepository(db).ListRecentPerApp(2)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint][]int{}
	for _, r := range rels {
		got[r.ApplicationID] = append(got[r.ApplicationID], r.Version)
	}
	for app, versions := range got {
		if len(versions) != 2 || (versions[0] != 4 && versions[1] != 4) || (versions[0] != 3 && versions[1] != 3) {
			t.Errorf("app %d versions = %v, want its newest two", app, versions)
		}
	}
	if len(got) != 2 {
		t.Fatalf("apps = %v", got)
	}
}

func TestRetentionPolicyDefaultsAndRunGuard(t *testing.T) {
	repo := NewRegistryRetentionRepository(retentionDB(t))
	p, err := repo.Policy()
	if err != nil || p.Enabled || p.KeepImages != 5 || len(p.ProtectedTags) != 1 {
		t.Fatalf("default policy = %+v, %v; it must start off, keeping 5", p, err)
	}

	first := &models.RegistryRetentionRun{Status: models.RetentionRunRunning, Trigger: "manual", StartedAt: time.Now()}
	if ok, err := repo.StartRun(first, time.Hour); !ok || err != nil {
		t.Fatalf("first run: %v %v", ok, err)
	}
	second := &models.RegistryRetentionRun{Status: models.RetentionRunRunning, Trigger: "manual", StartedAt: time.Now()}
	if ok, _ := repo.StartRun(second, time.Hour); ok {
		t.Fatal("a second run started while the first was running")
	}
	first.StartedAt = time.Now().Add(-2 * time.Hour)
	_ = repo.FinishRun(first)
	if ok, _ := repo.StartRun(second, time.Hour); !ok {
		t.Fatal("a run left running by a dead process must not block forever")
	}
}

func TestRetentionOverrideUpsert(t *testing.T) {
	repo := NewRegistryRetentionRepository(retentionDB(t))
	_ = repo.SaveOverride(&models.RegistryRetentionOverride{WorkspaceID: 4, KeepImages: 10})
	_ = repo.SaveOverride(&models.RegistryRetentionOverride{WorkspaceID: 4, Disabled: true})
	o, err := repo.Override(4)
	if err != nil || !o.Disabled || o.KeepImages != 0 {
		t.Fatalf("override = %+v, %v", o, err)
	}
	if o, _ := repo.Override(5); o.Disabled || o.KeepImages != 0 {
		t.Fatalf("a workspace with no row follows the platform, got %+v", o)
	}
}

func TestSavePolicyKeepsZeroValues(t *testing.T) {
	repo := NewRegistryRetentionRepository(retentionDB(t))
	if err := repo.SavePolicy(&models.RegistryRetention{KeepImages: 3, MinAgeDays: 0, ReleaseWindow: 0}); err != nil {
		t.Fatal(err)
	}
	p, _ := repo.Policy()
	if p.MinAgeDays != 0 || p.ReleaseWindow != 0 || p.KeepImages != 3 {
		t.Fatalf("policy = %+v; a deliberate 0 must not become the default", p)
	}
}
