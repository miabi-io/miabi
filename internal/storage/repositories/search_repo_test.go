// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type searchAppRow struct {
	ID          uint `gorm:"primaryKey"`
	UID         string
	WorkspaceID uint
	Name        string
	DisplayName string
	Status      string
}

func (searchAppRow) TableName() string { return "applications" }

type searchRouteRow struct {
	ID          uint `gorm:"primaryKey"`
	UID         string
	WorkspaceID uint
	Name        string
	DisplayName string
	Hosts       string
}

func (searchRouteRow) TableName() string { return "routes" }

type searchDomainRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	Name        string
}

func (searchDomainRow) TableName() string { return "domains" }

func newSearchDB(t *testing.T) *SearchRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&searchAppRow{}, &searchRouteRow{}, &searchDomainRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seed := []any{
		&searchAppRow{ID: 1, UID: "u1", WorkspaceID: 10, Name: "api-gateway", DisplayName: "API Gateway", Status: "running"},
		&searchAppRow{ID: 2, UID: "u2", WorkspaceID: 10, Name: "mb-7f3a", DisplayName: "Billing API", Status: "running"},
		&searchAppRow{ID: 3, UID: "u3", WorkspaceID: 20, Name: "api-other-workspace", DisplayName: "", Status: "running"},
		&searchRouteRow{ID: 1, UID: "r1", WorkspaceID: 10, Name: "api-public", Hosts: `["api.example.com"]`},
		&searchDomainRow{ID: 1, WorkspaceID: 10, Name: "api.example.com"},
	}
	for _, row := range seed {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return NewSearchRepository(db)
}

func TestSearchNeverLeavesTheWorkspace(t *testing.T) {
	repo := newSearchDB(t)
	hits, err := repo.Search(10, "api", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	for _, h := range hits {
		if h.Name == "api-other-workspace" {
			t.Errorf("a resource from another workspace was returned: %+v", h)
		}
	}
}

func TestSearchMatchesHandleAndDisplayName(t *testing.T) {
	repo := newSearchDB(t)
	hits, err := repo.Search(10, "billing", []string{"application"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Name != "mb-7f3a" {
		t.Fatalf("display-name match failed: %+v", hits)
	}
	if hits[0].Kind != "application" || hits[0].UID != "u2" {
		t.Errorf("hit is missing its identity: %+v", hits[0])
	}
	if hits[0].Detail != "running" {
		t.Errorf("detail = %q, want the app status", hits[0].Detail)
	}
}

func TestSearchSpansKinds(t *testing.T) {
	repo := newSearchDB(t)
	hits, err := repo.Search(10, "api.example.com", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, h := range hits {
		kinds[h.Kind] = true
	}
	for _, want := range []string{"route", "domain"} {
		if !kinds[want] {
			t.Errorf("no %s hit for a host that matches one: %+v", want, hits)
		}
	}
}

func TestSearchKindFilterNarrows(t *testing.T) {
	repo := newSearchDB(t)
	hits, err := repo.Search(10, "api", []string{"route"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Kind != "route" {
			t.Errorf("kind filter leaked a %s", h.Kind)
		}
	}
}

func TestSearchRespectsPerKindLimit(t *testing.T) {
	repo := newSearchDB(t)
	hits, err := repo.Search(10, "api", []string{"application"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("got %d hits, want 1", len(hits))
	}
}

func TestSearchIgnoresEmptyQueryAndUnscopedCalls(t *testing.T) {
	repo := newSearchDB(t)
	for _, tc := range []struct {
		ws uint
		q  string
	}{{10, ""}, {10, "   "}, {0, "api"}} {
		hits, err := repo.Search(tc.ws, tc.q, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 0 {
			t.Errorf("Search(%d, %q) returned %d hits", tc.ws, tc.q, len(hits))
		}
	}
}

func TestSearchSkipsTablesThatDoNotExist(t *testing.T) {
	repo := newSearchDB(t)
	if _, err := repo.Search(10, "api", nil, 10); err != nil {
		t.Fatalf("a missing table failed the whole search: %v", err)
	}
}
