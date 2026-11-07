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

type hcAppFixture struct {
	ID              uint `gorm:"primaryKey"`
	HealthcheckType string
}

func (hcAppFixture) TableName() string { return "applications" }

func TestHealthcheckImageStepPreservesBehaviour(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&hcAppFixture{}); err != nil {
		t.Fatal(err)
	}
	rows := []hcAppFixture{{ID: 1, HealthcheckType: "none"}, {ID: 2, HealthcheckType: ""}, {ID: 3, HealthcheckType: "http"}, {ID: 4, HealthcheckType: "command"}}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := healthcheckImageStep(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	want := map[uint]string{1: "image", 2: "image", 3: "http", 4: "command"}
	var got []hcAppFixture
	db.Order("id").Find(&got)
	for _, r := range got {
		if r.HealthcheckType != want[r.ID] {
			t.Errorf("app %d: type = %q, want %q", r.ID, r.HealthcheckType, want[r.ID])
		}
	}
}
