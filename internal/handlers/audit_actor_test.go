// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// "Who deployed this" must read as a person or a named service account, not a bare id a reader has to
// look up; a system action and a deleted actor stay without one.
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
