// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package updatecheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func rel(tag string, pre bool) Release {
	return Release{TagName: tag, HTMLURL: "https://example.test/" + tag, Prerelease: pre}
}

func TestNewestPicksBySemverNotString(t *testing.T) {
	// The whole point: lexically "v1.0.0-beta.9" > "v1.0.0-beta.10".
	rs := []Release{rel("v1.0.0-beta.9", true), rel("v1.0.0-beta.10", true)}
	got, ok := Newest("1.0.0-beta.4", rs)
	if !ok || got.TagName != "v1.0.0-beta.10" {
		t.Fatalf("got %q (ok=%v), want v1.0.0-beta.10", got.TagName, ok)
	}
}

func TestPrereleaseUserIsOfferedStable(t *testing.T) {
	// Lexically "v1.0.0-beta.4" > "v1.0.0"; semver says stable wins.
	rs := []Release{rel("v1.0.0", false)}
	got, ok := Newest("1.0.0-beta.4", rs)
	if !ok || got.TagName != "v1.0.0" {
		t.Fatalf("got %q (ok=%v), want v1.0.0", got.TagName, ok)
	}
}

func TestStableUserIsNeverOfferedAPrerelease(t *testing.T) {
	rs := []Release{rel("v1.1.0-rc.1", true)}
	if got, ok := Newest("1.0.0", rs); ok {
		t.Fatalf("stable build offered prerelease %q", got.TagName)
	}
	// ...but a newer stable is offered.
	rs = append(rs, rel("v1.1.0", false))
	got, ok := Newest("1.0.0", rs)
	if !ok || got.TagName != "v1.1.0" {
		t.Fatalf("got %q (ok=%v), want v1.1.0", got.TagName, ok)
	}
}

func TestUpToDateAndOlderReleasesIgnored(t *testing.T) {
	rs := []Release{rel("v1.0.0", false), rel("v0.9.0", false)}
	if got, ok := Newest("1.0.0", rs); ok {
		t.Fatalf("same version reported as newer: %q", got.TagName)
	}
}

func TestDraftsIgnored(t *testing.T) {
	r := rel("v2.0.0", false)
	r.Draft = true
	if got, ok := Newest("1.0.0", []Release{r}); ok {
		t.Fatalf("draft offered: %q", got.TagName)
	}
}

func TestDevBuildNeverChecks(t *testing.T) {
	if _, ok := Newest("dev", []Release{rel("v9.9.9", false)}); ok {
		t.Fatal("dev build was offered an update")
	}
	s := NewService(nil, "dev", true)
	if s.Enabled() {
		t.Fatal("dev build reports Enabled()")
	}
	if s := NewService(nil, "1.0.0", false); s.Enabled() {
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

// --- Check() against a fake GitHub ---

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

func TestCheckStoresNewerVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Spelled out, not compared against releasesPath: asserting a constant
		// against itself would pass even if someone "fixed" the path to
		// /releases/latest, which 404s for a repo whose releases are prereleases.
		if r.URL.Path != "/repos/miabi-io/miabi/releases" {
			t.Errorf("path = %q, want /repos/miabi-io/miabi/releases", r.URL.Path)
		}
		if got := r.URL.Query().Get("per_page"); got != "20" {
			t.Errorf("per_page = %q, want 20", got)
		}
		if ua := r.Header.Get("User-Agent"); ua != "miabi/1.0.0-beta.4" {
			t.Errorf("User-Agent = %q", ua)
		}
		if acc := r.Header.Get("Accept"); acc != "application/vnd.github+json" {
			t.Errorf("Accept = %q", acc)
		}
		// Nothing identifying may leak: no auth, no install id.
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization header sent to GitHub")
		}
		w.Header().Set("ETag", `W/"abc"`)
		_ = json.NewEncoder(w).Encode([]Release{rel("v1.0.0-beta.5", true)})
	}))
	defer srv.Close()

	s := NewService(testDB(t), "1.0.0-beta.4", true)
	s.setBaseURL(srv.URL)
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	st, _ := s.Status()
	if st.LatestVersion != "v1.0.0-beta.5" {
		t.Fatalf("LatestVersion = %q", st.LatestVersion)
	}
	if st.ETag != `W/"abc"` {
		t.Fatalf("ETag not stored: %q", st.ETag)
	}
	if st.CheckedAt == nil || st.LastError != "" {
		t.Fatalf("CheckedAt=%v LastError=%q", st.CheckedAt, st.LastError)
	}
}

func TestCheckReplaysETagAndKeepsCacheOn304(t *testing.T) {
	var sawIfNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	db := testDB(t)
	s := NewService(db, "1.0.0-beta.4", true)
	s.setBaseURL(srv.URL)

	// Seed a prior result; a 304 must preserve it. CheckedVersion matches the running
	// build, i.e. the verdict was computed for THIS binary — only then is replaying
	// the ETag sound. (When it does not match, see
	// TestUpgradedBuildIsNotOfferedTheReleaseItAlreadyPassed.)
	st, _ := s.Status()
	st.LatestVersion, st.ETag, st.CheckedVersion = "v1.0.0-beta.5", `W/"abc"`, "1.0.0-beta.4"
	if err := db.Save(st).Error; err != nil {
		t.Fatal(err)
	}

	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	if sawIfNoneMatch != `W/"abc"` {
		t.Fatalf("If-None-Match = %q, want the stored ETag", sawIfNoneMatch)
	}
	after, _ := s.Status()
	if after.LatestVersion != "v1.0.0-beta.5" {
		t.Fatalf("304 clobbered the cached verdict: %q", after.LatestVersion)
	}
	// A 304 is a SUCCESS, not a failure. Without this, treating non-2xx as an
	// error (e.g. via resp.Error()) still leaves LatestVersion untouched — the
	// assertion above passes and the regression slips through.
	if after.LastError != "" {
		t.Fatalf("304 recorded as an error: %q", after.LastError)
	}
}

// The reported bug: "Miabi v1.2.1 is available — you're running 1.3.0."
//
// The cached verdict depends on the release list AND the build it was compared
// against, but the ETag only fingerprints the list. Install 1.2.0, cache
// "v1.2.1 available", then upgrade to 1.3.0: the list has not changed, so
// replaying the ETag earns a 304 and the old verdict survives — permanently,
// since every later check 304s too. The notice then offers a DOWNGRADE, and no
// amount of waiting heals it.
//
// A build change must therefore invalidate the ETag.
func TestUpgradedBuildIsNotOfferedTheReleaseItAlreadyPassed(t *testing.T) {
	var sawIfNoneMatch string
	var served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		// GitHub would answer 304 to the stored ETag. Only reachable if we send it.
		if sawIfNoneMatch != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"abc"`)
		// The list is UNCHANGED — v1.2.1 is still the newest release. Nothing about
		// GitHub is different; the only thing that moved is the running build.
		_ = json.NewEncoder(w).Encode([]Release{rel("v1.2.1", false)})
	}))
	defer srv.Close()

	db := testDB(t)

	// The row as the 1.2.0 build left it: correct at the time it was written.
	s := NewService(db, "1.2.0", true)
	s.setBaseURL(srv.URL)
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("check as 1.2.0: %v", err)
	}
	if st, _ := s.Status(); st.LatestVersion != "v1.2.1" {
		t.Fatalf("precondition: LatestVersion = %q, want v1.2.1", st.LatestVersion)
	}

	// Now the operator upgrades the binary to 1.3.0. Same DB, same GitHub.
	s = NewService(db, "1.3.0", true)
	s.setBaseURL(srv.URL)
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("check as 1.3.0: %v", err)
	}

	if sawIfNoneMatch != "" {
		t.Errorf("replayed the ETag after the build changed (%q): the 304 would preserve a verdict computed for 1.2.0", sawIfNoneMatch)
	}
	if served != 2 {
		t.Errorf("GitHub hit %d times, want 2", served)
	}
	st, _ := s.Status()
	if st.LatestVersion != "" {
		t.Errorf("LatestVersion = %q; 1.3.0 is newer than every release, so there is no update to offer", st.LatestVersion)
	}
	if st.CheckedVersion != "1.3.0" {
		t.Errorf("CheckedVersion = %q, want 1.3.0 — the verdict must record the build it was computed for", st.CheckedVersion)
	}
}

// Even with a correct cache, the row is only rewritten by a daily cron: between an
// upgrade and the next tick it still describes the previous build. Readers must
// compare, so the notice can never propose a downgrade whatever is cached.
func TestIsNewerRejectsOlderAndEqual(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.3.0", "v1.2.1", false}, // the reported bug, at the read path
		{"1.3.0", "v1.3.0", false}, // up to date
		{"1.3.0", "v1.3.1", true},
		{"1.3.0", "v1.10.0", true},               // semver, not lexical: 10 > 3
		{"1.0.0-beta.9", "v1.0.0-beta.10", true}, // ditto, in the prerelease field
		{"dev", "v1.3.0", false},                 // a non-version build compares against nothing
		{"1.3.0", "", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestCheckClearsStalePointerWhenUpToDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Release{rel("v1.0.0", false)})
	}))
	defer srv.Close()

	db := testDB(t)
	s := NewService(db, "1.0.0", true)
	s.setBaseURL(srv.URL)
	st, _ := s.Status()
	st.LatestVersion, st.ReleaseURL = "v0.9.0", "https://old"
	_ = db.Save(st).Error

	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	after, _ := s.Status()
	if after.LatestVersion != "" || after.ReleaseURL != "" {
		t.Fatalf("stale pointer kept: %q %q", after.LatestVersion, after.ReleaseURL)
	}
}

func TestCheckRecordsErrorWithoutFailing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // rate limited
	}))
	defer srv.Close()

	s := NewService(testDB(t), "1.0.0", true)
	s.setBaseURL(srv.URL)
	// An air-gapped or rate-limited host must not turn cron red every day.
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("Check returned an error to cron: %v", err)
	}
	st, _ := s.Status()
	if st.LastError == "" {
		t.Fatal("failure not recorded on the row")
	}
	if st.CheckedAt == nil {
		t.Fatal("CheckedAt not stamped on a failed check")
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
