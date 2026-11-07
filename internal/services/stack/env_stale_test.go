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

package stack

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func release(id uint) *uint { return &id }

// A never-deployed app has no stale container to replace, so it stays unmarked.
func TestMarkMembersStaleSkipsAppsThatNeverDeployed(t *testing.T) {
	fake := &fakeApp{}
	s := NewService(nil, nil, nil, nil, fake, fakeVols{}, nil, nil)

	st := &models.Stack{ID: 1, Apps: []models.Application{
		{ID: 1, Name: "web", CurrentReleaseID: release(10)},
		{ID: 2, Name: "draft"}, // never deployed
		{ID: 3, Name: "worker", CurrentReleaseID: release(11)},
	}}

	if n := s.markMembersStale(st); n != 2 {
		t.Fatalf("marked %d apps, want 2", n)
	}
	if len(fake.marked) != 2 || fake.marked[0] != 1 || fake.marked[1] != 3 {
		t.Fatalf("marked %v, want the two deployed apps", fake.marked)
	}
}

func TestMarkMembersStaleOnAnEmptyStack(t *testing.T) {
	s := NewService(nil, nil, nil, nil, &fakeApp{}, fakeVols{}, nil, nil)
	if n := s.markMembersStale(&models.Stack{ID: 1}); n != 0 {
		t.Fatalf("marked %d apps on an empty stack, want 0", n)
	}
}

// A shared env change must not become a reason to restart apps that are already current.
func TestDeployAppsOutdatedOnlySkipsCurrentApps(t *testing.T) {
	fake := &fakeApp{}
	s := NewService(nil, nil, nil, nil, fake, fakeVols{}, nil, nil)

	apps := []models.Application{
		{ID: 1, Name: "web", RedeployRequired: true},
		{ID: 2, Name: "worker"},
		{ID: 3, Name: "cache", RedeployRequired: true},
	}

	results := s.deployApps(apps, true)
	if len(results) != 2 {
		t.Fatalf("%d deploys queued, want 2", len(results))
	}
	for _, id := range fake.deployed {
		if id == 2 {
			t.Fatal("redeployed an app that was already current")
		}
	}
}

func TestDeployAppsDeploysEverythingWhenNotFiltering(t *testing.T) {
	fake := &fakeApp{}
	s := NewService(nil, nil, nil, nil, fake, fakeVols{}, nil, nil)

	apps := []models.Application{
		{ID: 1, Name: "web", RedeployRequired: true},
		{ID: 2, Name: "worker"},
	}
	if results := s.deployApps(apps, false); len(results) != 2 {
		t.Fatalf("%d deploys queued, want 2", len(results))
	}
}

// app_env_vars carries no Postgres-only column defaults, so a plain sqlite fixture works.
func newOverrideService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.AppEnvVar{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewService(nil, repositories.NewApplicationRepository(db), nil, nil, &fakeApp{}, fakeVols{}, nil, nil)
	return s, db
}

// An app-level variable silently wins over the stack's, so the list must name the shadowing members.
func TestOverridesForNamesTheAppsThatShadowAKey(t *testing.T) {
	s, db := newOverrideService(t)
	rows := []models.AppEnvVar{
		{ApplicationID: 1, Key: "DATABASE_URL"},
		{ApplicationID: 3, Key: "DATABASE_URL"},
		{ApplicationID: 2, Key: "LOG_LEVEL"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed env vars: %v", err)
	}

	st := &models.Stack{ID: 1, Apps: []models.Application{
		{ID: 1, Name: "api"}, {ID: 2, Name: "worker"}, {ID: 3, Name: "web"},
	}}

	got := s.overridesFor(st)
	if len(got["DATABASE_URL"]) != 2 || got["DATABASE_URL"][0] != "api" || got["DATABASE_URL"][1] != "web" {
		t.Fatalf("DATABASE_URL overridden by %v, want [api web] in name order", got["DATABASE_URL"])
	}
	if len(got["LOG_LEVEL"]) != 1 || got["LOG_LEVEL"][0] != "worker" {
		t.Fatalf("LOG_LEVEL overridden by %v, want [worker]", got["LOG_LEVEL"])
	}
	if _, ok := got["UNSET"]; ok {
		t.Fatal("a key no app defines must not appear")
	}
}

// A member outside the stack must not be reported as overriding it.
func TestOverridesForIgnoresAppsOutsideTheStack(t *testing.T) {
	s, db := newOverrideService(t)
	if err := db.Create(&models.AppEnvVar{ApplicationID: 99, Key: "DATABASE_URL"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	st := &models.Stack{ID: 1, Apps: []models.Application{{ID: 1, Name: "api"}}}
	if got := s.overridesFor(st); len(got) != 0 {
		t.Fatalf("overrides = %v, want none", got)
	}
}

func TestOverridesForOnAStackWithNoApps(t *testing.T) {
	s, _ := newOverrideService(t)
	if got := s.overridesFor(&models.Stack{ID: 1}); got != nil {
		t.Fatalf("overrides = %v, want nil", got)
	}
}
