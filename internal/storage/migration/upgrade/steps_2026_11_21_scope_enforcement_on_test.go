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

func TestScopeEnforcementOnStep(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := scopeEnforcementOnStep(context.Background(), db); err != nil {
		t.Fatalf("without a settings table: %v", err)
	}
	db.Exec(`CREATE TABLE settings (id INTEGER PRIMARY KEY, key TEXT, value TEXT)`)
	db.Exec(`INSERT INTO settings (key, value) VALUES (?, 'warn'), ('other', 'x')`, legacyScopeModeSetting)
	if err := scopeEnforcementOnStep(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var keys []string
	db.Raw(`SELECT key FROM settings`).Scan(&keys)
	if len(keys) != 1 || keys[0] != "other" {
		t.Fatalf("settings left = %v, want only other", keys)
	}
}
