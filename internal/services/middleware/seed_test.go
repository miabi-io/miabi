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

package middleware

import (
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/mwcatalog"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newSeedService(t *testing.T) (*Service, *repositories.MiddlewareRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Middleware{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := repositories.NewMiddlewareRepository(db)
	return NewService(repo, nil), repo // nil syncer: SeedDefaults is nil-safe on sync
}

func TestSeedDefaultsCreatesPolicies(t *testing.T) {
	svc, repo := newSeedService(t)

	if err := svc.SeedDefaults(context.Background(), 1); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}
	got, err := repo.ListByWorkspace(1)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(mwcatalog.DefaultSeed()); len(got) != want {
		t.Fatalf("seeded %d policies, want %d", len(got), want)
	}
}

func TestSeedDefaultsIsIdempotent(t *testing.T) {
	svc, repo := newSeedService(t)
	ctx := context.Background()

	if err := svc.SeedDefaults(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// A second call must not duplicate (workspace already has policies).
	if err := svc.SeedDefaults(ctx, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.ListByWorkspace(1)
	if want := len(mwcatalog.DefaultSeed()); len(got) != want {
		t.Fatalf("after re-seed have %d policies, want %d (no duplication)", len(got), want)
	}
}

func TestSeedDefaultsSkipsWhenPoliciesExist(t *testing.T) {
	svc, repo := newSeedService(t)
	// A pre-existing user policy means the workspace is not new, so seeding is skipped entirely.
	if err := repo.Create(&models.Middleware{WorkspaceID: 2, Name: "mine", Type: "access", Rule: map[string]any{"statusCode": 403}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SeedDefaults(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.ListByWorkspace(2)
	if len(got) != 1 {
		t.Fatalf("expected only the pre-existing policy, got %d", len(got))
	}
}
