// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// A 0 meant "unlimited" under the old rule and means "none allowed" under the new one, so it has to
// become -1. A real count must survive untouched.
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

// A value that is neither a count nor a sentinel must be left alone rather than cast. Postgres
// errors on CAST('ten' AS INTEGER), and a step that fails blocks the boot it was meant to upgrade.
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

// A negative value other than -1 also meant unlimited, and the step must not churn an already
// converted row on the next boot.
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
