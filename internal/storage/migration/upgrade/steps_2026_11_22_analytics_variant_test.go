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

func TestAnalyticsVariantKeyStep(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := analyticsVariantKeyStep(context.Background(), db); err != nil {
		t.Fatalf("without the table: %v", err)
	}
	// The table as AutoMigrate leaves it on an upgraded install: variant added, the old four-column key kept.
	for _, q := range []string{
		`CREATE TABLE analytics_rollups (id INTEGER PRIMARY KEY, workspace_id INTEGER NOT NULL, application_id INTEGER,
			route_name TEXT, variant TEXT NOT NULL DEFAULT '', bucket DATETIME, requests INTEGER)`,
		`CREATE UNIQUE INDEX idx_arollup ON analytics_rollups (workspace_id, application_id, route_name, bucket)`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	insert := func(variant string) error {
		return db.Exec(`INSERT INTO analytics_rollups (workspace_id, application_id, route_name, variant, bucket, requests)
			VALUES (1, 2, 'mb-ws1-api', ?, '2026-11-22 10:00:00', 1)`, variant).Error
	}
	if err := insert("stable"); err != nil {
		t.Fatal(err)
	}
	if insert("canary") == nil {
		t.Fatal("old key should reject a second variant of the same minute")
	}

	if err := analyticsVariantKeyStep(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := insert("canary"); err != nil {
		t.Fatalf("new key rejected the canary row: %v", err)
	}
	if insert("canary") == nil {
		t.Fatal("new key accepted a duplicate canary row")
	}
}
