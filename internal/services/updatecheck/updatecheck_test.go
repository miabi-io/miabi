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

package updatecheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func rel(v string) *Release {
	return &Release{Version: v, ReleaseURL: "https://example.test/" + v, Reason: "bugfix", ReleasedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:uc_"+t.Name()+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&models.UpdateStatus{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// serve answers with idx, honouring If-None-Match against etag, and counts requests.
func serve(t *testing.T, idx *Index, etag string) (*httptest.Server, *int, *string) {
	t.Helper()
	var hits int
	var sawIfNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		if etag != "" && sawIfNoneMatch == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(idx)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &sawIfNoneMatch
}

func checker(srv *httptest.Server, version string, services ...*Service) *Checker {
	c := NewChecker(version, services...)
	c.setURL(srv.URL, version)
	return c
}

func TestPickForTheRunningBuild(t *testing.T) {
	cases := []struct {
		name, current string
		ch            Channels
		want          string
	}{
		{"semver, not lexical", "1.0.0-beta.4", Channels{Prerelease: rel("1.0.0-beta.10")}, "1.0.0-beta.10"},
		{"prerelease user is offered stable", "1.0.0-beta.4", Channels{Stable: rel("1.0.0"), Prerelease: rel("1.1.0-beta.1")}, "1.1.0-beta.1"},
		{"stable user never offered a prerelease", "1.0.0", Channels{Prerelease: rel("1.1.0-rc.1")}, ""},
		{"stable user offered newer stable", "1.0.0", Channels{Stable: rel("1.1.0"), Prerelease: rel("1.2.0-rc.1")}, "1.1.0"},
		{"up to date", "1.1.0", Channels{Stable: rel("1.1.0")}, ""},
		{"ahead of the index", "1.2.0", Channels{Stable: rel("1.1.0")}, ""},
		{"prerelease mislabelled as stable", "1.0.0", Channels{Stable: rel("1.1.0-rc.1")}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NewService(nil, c.current, true).pick(c.ch)
			if v := got.versionOrEmpty(); v != c.want {
				t.Errorf("pick = %q, want %q", v, c.want)
			}
		})
	}
}

// Components this process does not run follow the stable channel only, whatever the index says.
func TestComponentsFollowStableOnly(t *testing.T) {
	s := NewComponentService(nil, ComponentRunner, true)
	if got := s.pick(Channels{Stable: rel("0.2.0"), Prerelease: rel("0.3.0-rc.1")}); got.versionOrEmpty() != "0.2.0" {
		t.Errorf("runner picked %q, want 0.2.0", got.versionOrEmpty())
	}
	if got := s.pick(Channels{Stable: rel("0.3.0-rc.1")}); got != nil {
		t.Errorf("a prerelease in the stable channel reached a component: %q", got.Version)
	}
}

func TestDevBuildNeverChecks(t *testing.T) {
	if NewService(nil, "dev", true).Enabled() {
		t.Fatal("dev build reports Enabled()")
	}
	if NewService(nil, "1.0.0", false).Enabled() {
		t.Fatal("disabled service reports Enabled()")
	}
}

func TestNormalizeHandlesBakedTagWithoutV(t *testing.T) {
	if got := normalize("1.0.0-beta.4"); got != "v1.0.0-beta.4" {
		t.Fatalf("normalize = %q", got)
	}
	for _, bad := range []string{"dev", "unknown", "", "a1b2c3d"} {
		if got := normalize(bad); got != "" {
			t.Fatalf("normalize(%q) = %q, want empty", bad, got)
		}
	}
}

// One request fills every component's row, and the request says nothing about the install.
func TestCheckStoresEveryComponentFromOneRequest(t *testing.T) {
	miabi := rel("1.11.0")
	miabi.Reason, miabi.ChangelogURL = ReasonSecurity, "https://docs.example.test/1.11"
	idx := &Index{Components: map[string]Channels{
		ComponentMiabi:   {Stable: miabi},
		ComponentAgent:   {Stable: rel("0.6.2")},
		ComponentRunner:  {Stable: rel("0.2.0")},
		ComponentGateway: {Stable: rel("1.1.1")},
	}}
	var ua, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua, auth = r.Header.Get("User-Agent"), r.Header.Get("Authorization")
		if r.URL.RawQuery != "" {
			t.Errorf("query string sent: %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(idx)
	}))
	defer srv.Close()

	db := testDB(t)
	mi := NewService(db, "1.10.18", true)
	ag := NewAgentService(db, true)
	ru := NewComponentService(db, ComponentRunner, true)
	gw := NewComponentService(db, ComponentGateway, true)
	if err := checker(srv, "1.10.18", mi, ag, ru, gw).Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	if ua != "miabi/1.10.18" || auth != "" {
		t.Errorf("User-Agent=%q Authorization=%q", ua, auth)
	}

	st, _ := mi.Status()
	if st.LatestVersion != "v1.11.0" || st.Reason != ReasonSecurity || st.ReleaseURL != "https://docs.example.test/1.11" {
		t.Errorf("miabi row = %+v", st)
	}
	for svc, want := range map[*Service]string{ag: "v0.6.2", ru: "v0.2.0", gw: "v1.1.1"} {
		if got := svc.LatestVersion(); got != want {
			t.Errorf("%s latest = %q, want %q", svc.Component(), got, want)
		}
	}
	var n int64
	db.Model(&models.UpdateStatus{}).Count(&n)
	if n != 4 {
		t.Errorf("got %d rows, want one per component", n)
	}
}

func TestCheckReplaysETagAndKeepsCacheOn304(t *testing.T) {
	idx := &Index{Components: map[string]Channels{ComponentMiabi: {Stable: rel("1.0.1")}}}
	srv, hits, sawIfNoneMatch := serve(t, idx, `"abc"`)
	db := testDB(t)
	c := checker(srv, "1.0.0", NewService(db, "1.0.0", true), NewAgentService(db, true))

	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *hits != 2 || *sawIfNoneMatch != `"abc"` {
		t.Fatalf("hits=%d If-None-Match=%q, want the stored ETag replayed", *hits, *sawIfNoneMatch)
	}
	st, _ := NewService(db, "1.0.0", true).Status()
	// A 304 is a success: the verdict survives and no error is recorded.
	if st.LatestVersion != "v1.0.1" || st.LastError != "" {
		t.Fatalf("after 304: LatestVersion=%q LastError=%q", st.LatestVersion, st.LastError)
	}
}

// The ETag fingerprints only the index, so after an upgrade a 304 would keep offering a downgrade.
func TestUpgradedBuildIsNotOfferedTheReleaseItAlreadyPassed(t *testing.T) {
	idx := &Index{Components: map[string]Channels{ComponentMiabi: {Stable: rel("1.2.1")}}}
	srv, hits, sawIfNoneMatch := serve(t, idx, `"abc"`)
	db := testDB(t)

	if err := checker(srv, "1.2.0", NewService(db, "1.2.0", true)).Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, _ := NewService(db, "1.2.0", true).Status(); st.LatestVersion != "v1.2.1" {
		t.Fatalf("precondition: LatestVersion = %q", st.LatestVersion)
	}

	upgraded := NewService(db, "1.3.0", true)
	if err := checker(srv, "1.3.0", upgraded).Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *sawIfNoneMatch != "" || *hits != 2 {
		t.Errorf("replayed the ETag after the build changed (%q, %d hits)", *sawIfNoneMatch, *hits)
	}
	st, _ := upgraded.Status()
	if st.LatestVersion != "" || st.CheckedVersion != "1.3.0" {
		t.Errorf("LatestVersion=%q CheckedVersion=%q; 1.3.0 is newer than the index", st.LatestVersion, st.CheckedVersion)
	}
}

func TestIsNewerRejectsOlderAndEqual(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.3.0", "v1.2.1", false},
		{"1.3.0", "v1.3.0", false},
		{"1.3.0", "v1.3.1", true},
		{"1.3.0", "v1.10.0", true},
		{"1.0.0-beta.9", "v1.0.0-beta.10", true},
		{"dev", "v1.3.0", false},
		{"1.3.0", "", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestCheckClearsStalePointerWhenUpToDate(t *testing.T) {
	srv, _, _ := serve(t, &Index{Components: map[string]Channels{ComponentMiabi: {Stable: rel("1.0.0")}}}, "")
	db := testDB(t)
	s := NewService(db, "1.0.0", true)
	st, _ := s.Status()
	st.LatestVersion, st.ReleaseURL, st.Reason = "v0.9.0", "https://old", ReasonSecurity
	_ = db.Save(st).Error

	if err := checker(srv, "1.0.0", s).Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Status()
	if after.LatestVersion != "" || after.ReleaseURL != "" || after.Reason != "" {
		t.Fatalf("stale pointer kept: %+v", after)
	}
}

func TestCheckRecordsErrorWithoutFailing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	db := testDB(t)
	mi, ag := NewService(db, "1.0.0", true), NewAgentService(db, true)
	// An air-gapped host must not turn cron red every day.
	if err := checker(srv, "1.0.0", mi, ag).Check(context.Background()); err != nil {
		t.Fatalf("Check returned an error to cron: %v", err)
	}
	for _, s := range []*Service{mi, ag} {
		st, _ := s.Status()
		if st.LastError == "" || st.CheckedAt == nil {
			t.Errorf("%s: failure not recorded: %+v", s.Component(), st)
		}
	}
}

func TestDisabledCheckerFetchesNothing(t *testing.T) {
	srv, hits, _ := serve(t, &Index{}, "")
	db := testDB(t)
	c := checker(srv, "1.0.0", NewService(db, "1.0.0", false), NewAgentService(db, false))
	if c.Enabled() {
		t.Fatal("checker enabled with every component off")
	}
	if err := c.Check(context.Background()); err != nil || *hits != 0 {
		t.Fatalf("err=%v hits=%d, want no request", err, *hits)
	}
}

func TestDismissIsPerVersion(t *testing.T) {
	s := NewService(testDB(t), "1.0.0", true)
	if err := s.Dismiss("v1.1.0"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	st, _ := s.Status()
	if st.DismissedVersion != "v1.1.0" {
		t.Fatalf("DismissedVersion = %q", st.DismissedVersion)
	}
}
