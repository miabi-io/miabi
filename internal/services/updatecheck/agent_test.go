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

package updatecheck

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestClassifyAgent(t *testing.T) {
	cases := []struct {
		name            string
		current, latest string
		want            AgentState
	}{
		{"never reported", "", "0.5.0", AgentUnknown},
		{"dev build is not a verdict", "dev", "0.5.0", AgentUnknown},
		{"below the floor", "0.3.0", "0.5.0", AgentUnsupported},
		{"below the floor with no release data", "0.3.0", "", AgentUnsupported},
		{"at the floor but behind", "0.4.0", "0.5.0", AgentOutdated},
		{"newest", "0.5.0", "0.5.0", AgentCurrent},
		{"ahead of what we know", "0.6.0", "0.5.0", AgentCurrent},
		{"no release data and supported", "0.5.0", "", AgentCurrent},
		{"v-prefixed both sides", "v0.4.0", "v0.5.0", AgentOutdated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyAgent(c.current, c.latest); got != c.want {
				t.Errorf("ClassifyAgent(%q, %q) = %q, want %q", c.current, c.latest, got, c.want)
			}
		})
	}
}

// The floor is the half that works with no network, so it must not depend on a release list.
func TestAgentSupportedNeedsNoReleaseData(t *testing.T) {
	if AgentSupported("0.3.0") {
		t.Error("0.3.0 is below the floor but reported supported")
	}
	if !AgentSupported(MinAgentVersion) {
		t.Errorf("the floor %q reports itself unsupported", MinAgentVersion)
	}
	// An unreadable version is not evidence of being old.
	if !AgentSupported("some-commit-sha") {
		t.Error("an unparseable version was called unsupported")
	}
}

// Every node reports its own version, so the agent check must not disable itself like a dev platform build.
func TestAgentServiceEnabledWithoutALocalBuild(t *testing.T) {
	if !NewAgentService(nil, true).Enabled() {
		t.Error("the agent check disabled itself for having no local version")
	}
	if NewAgentService(nil, false).Enabled() {
		t.Error("the agent check ran while update checks are switched off")
	}
}

// Deliberately maps onto the same table as models.UpdateStatus.
type legacyUpdateStatus struct {
	ID            uint `gorm:"primaryKey"`
	LatestVersion string
	ReleaseURL    string
	ETag          string
}

func (legacyUpdateStatus) TableName() string { return "update_statuses" }

// AutoMigrate must add a NOT NULL column to a populated table; getting this wrong fails at boot.
func TestMigratingAnInstallThatPredatesComponents(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:uc_migrate_"+t.Name()+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&legacyUpdateStatus{}); err != nil {
		t.Fatalf("migrate legacy shape: %v", err)
	}
	if err := db.Create(&legacyUpdateStatus{ID: 1, LatestVersion: "v1.2.3", ETag: "W/\"abc\""}).Error; err != nil {
		t.Fatalf("seed the legacy row: %v", err)
	}

	if err := db.AutoMigrate(&models.UpdateStatus{}); err != nil {
		t.Fatalf("migrate to the component shape: %v", err)
	}

	var rows []models.UpdateStatus
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows after migrating, want the one that was there", len(rows))
	}
	if rows[0].Component != ComponentMiabi {
		t.Errorf("the pre-existing row came through as %q, want %q — the platform's own verdict was orphaned",
			rows[0].Component, ComponentMiabi)
	}
	if rows[0].LatestVersion != "v1.2.3" {
		t.Errorf("the migration lost the cached verdict: %q", rows[0].LatestVersion)
	}

	// And the agent's row is created beside it rather than colliding with it.
	if _, err := NewAgentService(db, true).Status(); err != nil {
		t.Fatalf("create the agent row alongside: %v", err)
	}
	var n int64
	if err := db.Model(&models.UpdateStatus{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("got %d rows, want one per component", n)
	}
}
