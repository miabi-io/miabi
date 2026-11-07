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

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type dbURLInstFixture struct {
	ID     uint `gorm:"primaryKey"`
	Name   string
	Engine string
}

func (dbURLInstFixture) TableName() string { return "database_instances" }

type dbURLDBFixture struct {
	ID            uint `gorm:"primaryKey"`
	InstanceID    uint
	Name          string
	ApplicationID *uint
	EnvPrefix     string
}

func (dbURLDBFixture) TableName() string { return "databases" }

func TestDBURLEnvStep(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&dbURLInstFixture{}, &dbURLDBFixture{}, &models.AppEnvVar{}); err != nil {
		t.Fatal(err)
	}
	a1, a2, a3 := uint(1), uint(2), uint(3)
	db.Create(&dbURLInstFixture{ID: 1, Name: "main", Engine: "postgres"})
	db.Create(&[]dbURLDBFixture{
		{ID: 1, InstanceID: 1, Name: "shop", ApplicationID: &a1},
		{ID: 2, InstanceID: 1, Name: "stats", ApplicationID: &a2, EnvPrefix: "ANALYTICS"},
		{ID: 3, InstanceID: 1, Name: "blog", ApplicationID: &a3},
		{ID: 4, InstanceID: 1, Name: "free"},
	})
	db.Create(&[]models.AppEnvVar{
		{ApplicationID: 1, Key: "DATABASE_URL", Value: "${{ secrets.db_main_shop_url }}"},
		{ApplicationID: 2, Key: "ANALYTICS_DATABASE_URL", Value: "${{ secrets.db_main_stats_url }}"},
		{ApplicationID: 3, Key: "DATABASE_URL", Value: "postgres://hand/typed"},
	})
	if err := dbURLEnvStep(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := dbURLEnvStep(context.Background(), db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var got []models.AppEnvVar
	db.Where("key LIKE ?", "%DB_URL").Order("application_id").Find(&got)
	if len(got) != 2 || got[0].Key != "DB_URL" || got[0].Value != "${{ secrets.db_main_shop_url }}" ||
		got[1].Key != "ANALYTICS_DB_URL" || got[1].Value != "${{ secrets.db_main_stats_url }}" {
		t.Fatalf("DB_URL rows = %+v", got)
	}
}
