// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// A shared env var reaches a member's container only at deploy time, so changing
// one must leave every already-deployed member marked — and an app that has never
// been deployed untouched, since it has no stale container to replace.
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

// Tracking staleness only pays off if acting on it is precise: a shared env change
// must not become a reason to restart apps that are already current.
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

// overridesFor needs the real application repository, so it is exercised through
// its own sqlite fixture; app_env_vars carries no Postgres-only column defaults.
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

// An app-level variable silently wins over the stack's, so the list has to name
// the members a shared value will never reach.
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
