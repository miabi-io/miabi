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

package handlers

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWithActorsResolvesPeopleAndServiceAccounts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	users := repositories.NewUserRepository(db)
	ws := uint(1)
	person := &models.User{Name: "Ada", Username: "ada", Email: "ada@example.com", Kind: models.UserKindHuman, Active: true}
	bot := &models.User{Name: "ci", Username: "sa-ci", Email: "sa-ci@" + models.ServiceAccountEmailDomain,
		Kind: models.UserKindService, ServiceWorkspaceID: &ws, Active: true}
	for _, u := range []*models.User{person, bot} {
		if err := users.Create(u); err != nil {
			t.Fatal(err)
		}
	}
	gone := uint(999)
	entries := []models.AuditLog{
		{ID: 1, ActorID: &person.ID, Action: "app.deploy"},
		{ID: 2, ActorID: &bot.ID, Action: "app.deploy"},
		{ID: 3, ActorID: &person.ID, Action: "app.restart"},
		{ID: 4, Action: "backup.run"},
		{ID: 5, ActorID: &gone, Action: "app.delete"},
	}

	got := withActors(users, entries)
	if len(got) != len(entries) {
		t.Fatalf("%d entries, want %d", len(got), len(entries))
	}
	if a := got[0].Actor; a == nil || a.Name != "Ada" || a.Kind != models.UserKindHuman {
		t.Errorf("person actor = %+v", a)
	}
	if a := got[1].Actor; a == nil || a.Name != "ci" || a.Kind != models.UserKindService {
		t.Errorf("service account actor = %+v, want kind service", a)
	}
	if got[2].Actor == nil || got[2].Actor != got[0].Actor {
		t.Error("a repeated actor should resolve once and be shared")
	}
	if got[3].Actor != nil || got[4].Actor != nil {
		t.Errorf("system/deleted actors = %+v, %+v; want none", got[3].Actor, got[4].Actor)
	}
}
