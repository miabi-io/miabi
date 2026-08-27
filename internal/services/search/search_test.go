// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"testing"

	"github.com/miabi-io/miabi/internal/storage/repositories"
)

func TestSplitKind(t *testing.T) {
	cases := []struct {
		in        string
		wantKind  string
		wantQuery string
	}{
		{"api", "", "api"},
		{"app:api", "application", "api"},
		{"apps: api", "application", "api"},
		{"DB:main", "database", "main"},
		{"repo:miabi", "gitrepository", "miabi"},
		{"gitops:infra", "gitsource", "infra"},
		{"nope:api", "", "nope:api"},
		{":api", "", ":api"},
		{"host:port", "", "host:port"},
	}
	for _, tc := range cases {
		kind, query := splitKind(tc.in)
		if kind != tc.wantKind || query != tc.wantQuery {
			t.Errorf("splitKind(%q) = (%q, %q), want (%q, %q)", tc.in, kind, query, tc.wantKind, tc.wantQuery)
		}
	}
}

func TestScoreRanksExactOverPrefixOverSubstring(t *testing.T) {
	exact := score("api", repositories.SearchHit{Name: "api"})
	prefix := score("api", repositories.SearchHit{Name: "api-gateway"})
	substr := score("api", repositories.SearchHit{Name: "legacy-api-proxy"})
	if !(exact > prefix && prefix > substr) {
		t.Errorf("ranking is wrong: exact=%d prefix=%d substring=%d", exact, prefix, substr)
	}
}

func TestScoreUsesDisplayNameWhenTheHandleDoesNotMatch(t *testing.T) {
	hit := repositories.SearchHit{Name: "mb-7f3a", DisplayName: "Billing API"}
	if score("billing", hit) <= 10 {
		t.Error("a display-name match scored no better than no match")
	}
}

func TestCleanDetailRendersRouteHosts(t *testing.T) {
	got := cleanDetail("route", `["api.example.com","www.example.com"]`)
	if got != "api.example.com, www.example.com" {
		t.Errorf("cleanDetail = %q", got)
	}
	if got := cleanDetail("secret", "a description"); got != "a description" {
		t.Errorf("non-route detail was rewritten: %q", got)
	}
	if got := cleanDetail("route", "not json"); got != "not json" {
		t.Errorf("unparseable hosts were dropped: %q", got)
	}
}

func TestShortQueriesReturnNothing(t *testing.T) {
	s := NewService(nil)
	for _, q := range []string{"", " ", "a", "app:x"} {
		res, err := s.Search(1, q, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Results) != 0 {
			t.Errorf("query %q returned %d results without hitting the database", q, len(res.Results))
		}
	}
}

func TestNoWorkspaceReturnsNothing(t *testing.T) {
	s := NewService(nil)
	res, err := s.Search(0, "api", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 0 {
		t.Errorf("an unscoped search returned %d results", len(res.Results))
	}
}
