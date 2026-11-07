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

package upgrade

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func limitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&settingFixture{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func valueOf(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	var row settingFixture
	if err := db.Where("key = ?", key).First(&row).Error; err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return row.Value
}

func TestWorkspaceLimitSentinelPreservesUnlimited(t *testing.T) {
	db := limitDB(t)
	rows := []settingFixture{
		{ID: 1, Key: "max_workspaces_per_user", Value: "0"},
		{ID: 2, Key: "max_workspace_memberships_per_user", Value: "3"},
		{ID: 3, Key: "audit_log_retention_days", Value: "0"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	if err := workspaceLimitSentinelStep(context.Background(), db); err != nil {
		t.Fatalf("step: %v", err)
	}

	if got := valueOf(t, db, "max_workspaces_per_user"); got != "-1" {
		t.Errorf("a stored 0 = %q, want -1 so it keeps meaning unlimited", got)
	}
	if got := valueOf(t, db, "max_workspace_memberships_per_user"); got != "3" {
		t.Errorf("a real count = %q, want it untouched", got)
	}
	if got := valueOf(t, db, "audit_log_retention_days"); got != "0" {
		t.Errorf("an unrelated key = %q, want it untouched", got)
	}
}

// Postgres errors on CAST('ten' AS INTEGER), and a failing step blocks the boot it was meant to upgrade.
func TestWorkspaceLimitSentinelLeavesUnparseableValues(t *testing.T) {
	db := limitDB(t)
	if err := db.Create(&[]settingFixture{
		{ID: 1, Key: "max_workspaces_per_user", Value: "ten"},
		{ID: 2, Key: "max_workspace_memberships_per_user", Value: ""},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := workspaceLimitSentinelStep(context.Background(), db); err != nil {
		t.Fatalf("step must not fail on an unparseable value: %v", err)
	}
	if got := valueOf(t, db, "max_workspaces_per_user"); got != "ten" {
		t.Errorf("unparseable value = %q, want it untouched", got)
	}
	if got := valueOf(t, db, "max_workspace_memberships_per_user"); got != "" {
		t.Errorf("empty value = %q, want it untouched", got)
	}
}

func TestWorkspaceLimitSentinelIsIdempotent(t *testing.T) {
	db := limitDB(t)
	if err := db.Create(&[]settingFixture{
		{ID: 1, Key: "max_workspaces_per_user", Value: "-5"},
		{ID: 2, Key: "max_workspace_memberships_per_user", Value: "-1"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := workspaceLimitSentinelStep(context.Background(), db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	for _, k := range workspaceLimitSentinelKeys {
		if got := valueOf(t, db, k); got != "-1" {
			t.Errorf("%s = %q, want -1", k, got)
		}
	}
}
