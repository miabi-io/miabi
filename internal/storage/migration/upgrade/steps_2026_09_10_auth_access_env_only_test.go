// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// settingFixture mirrors the columns the step touches.
type settingFixture struct {
	ID    uint `gorm:"primaryKey"`
	Key   string
	Value string
}

func (settingFixture) TableName() string { return "settings" }

func TestAuthAccessEnvOnlyDropsOnlyItsOwnRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&settingFixture{}); err != nil {
		t.Fatal(err)
	}
	rows := []settingFixture{
		{ID: 1, Key: "registration_enabled", Value: "true"},
		{ID: 2, Key: "require_email_verification", Value: "true"},
		{ID: 3, Key: "allowed_signup_domains", Value: "acme.com"},
		{ID: 4, Key: "maintenance_mode", Value: "false"},
		{ID: 5, Key: "password_reset_enabled", Value: "true"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	if err := authAccessEnvOnlyStep(context.Background(), db); err != nil {
		t.Fatalf("step: %v", err)
	}

	var got []settingFixture
	if err := db.Order("id").Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	want := []string{"require_email_verification", "allowed_signup_domains", "maintenance_mode"}
	if len(got) != len(want) {
		t.Fatalf("read back %d rows, want %d", len(got), len(want))
	}
	for i, r := range got {
		if r.Key != want[i] {
			t.Errorf("row %d = %q, want %q", i, r.Key, want[i])
		}
	}
}

// Steps run on every boot, including installs that never had the row.
func TestAuthAccessEnvOnlyIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&settingFixture{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := authAccessEnvOnlyStep(context.Background(), db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
}
