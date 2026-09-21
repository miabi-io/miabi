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

// The agent channel is stable-only, and it must hold whether or not the maintainer remembered to
// tick GitHub's prerelease box. The agent carries a real "v0.1-rc.1" tag that predates the habit.
func TestAgentChannelExcludesPrereleaseTags(t *testing.T) {
	rs := []Release{
		rel("v0.4.0", false),
		rel("v0.5.0-rc.1", true),    // flagged
		rel("v0.6.0-beta.2", false), // NOT flagged — only the tag gives it away
		rel("v0.1-rc.1", false),
	}
	got, ok := Latest(rs, true)
	if !ok || got.TagName != "v0.4.0" {
		t.Fatalf("stable-only picked %q (ok=%v), want v0.4.0", got.TagName, ok)
	}
	// Without the restriction the newest wins, prerelease or not.
	got, ok = Latest(rs, false)
	if !ok || got.TagName != "v0.6.0-beta.2" {
		t.Fatalf("unrestricted picked %q (ok=%v), want v0.6.0-beta.2", got.TagName, ok)
	}
}

// A stable platform build must not be offered an unflagged release candidate either.
func TestNewestIgnoresUnflaggedPrereleaseTag(t *testing.T) {
	if got, ok := Newest("1.0.0", []Release{rel("v1.1.0-rc.1", false)}); ok {
		t.Fatalf("stable build offered unflagged prerelease %q", got.TagName)
	}
}

func TestLatestSkipsDraftsAndJunkTags(t *testing.T) {
	draft := rel("v9.9.9", false)
	draft.Draft = true
	rs := []Release{draft, rel("nightly", false), rel("v0.4.0", false)}
	got, ok := Latest(rs, true)
	if !ok || got.TagName != "v0.4.0" {
		t.Fatalf("got %q (ok=%v), want v0.4.0", got.TagName, ok)
	}
}

func TestClassifyAgent(t *testing.T) {
	cases := []struct {
		name            string
		current, latest string
		want            AgentState
	}{
		{"never reported", "", "0.5.0", AgentUnknown},
		{"dev build is not a verdict", "dev", "0.5.0", AgentUnknown},
		{"below the floor", "0.3.0", "0.5.0", AgentUnsupported},
		{"below the floor with no release data", "0.3.0", "", AgentUnsupported},
		{"at the floor but behind", "0.4.0", "0.5.0", AgentOutdated},
		{"newest", "0.5.0", "0.5.0", AgentCurrent},
		{"ahead of what we know", "0.6.0", "0.5.0", AgentCurrent},
		{"no release data and supported", "0.5.0", "", AgentCurrent},
		{"v-prefixed both sides", "v0.4.0", "v0.5.0", AgentOutdated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyAgent(c.current, c.latest); got != c.want {
				t.Errorf("ClassifyAgent(%q, %q) = %q, want %q", c.current, c.latest, got, c.want)
			}
		})
	}
}

// The floor is the half that works with no network, so it must not depend on a release list.
func TestAgentSupportedNeedsNoReleaseData(t *testing.T) {
	if AgentSupported("0.3.0") {
		t.Error("0.3.0 is below the floor but reported supported")
	}
	if !AgentSupported(MinAgentVersion) {
		t.Errorf("the floor %q reports itself unsupported", MinAgentVersion)
	}
	// An unreadable version is not evidence of being old.
	if !AgentSupported("some-commit-sha") {
		t.Error("an unparseable version was called unsupported")
	}
}

// Each component keeps its own row, so the agent's verdict cannot overwrite the platform's.
func TestComponentsAreStoredSeparately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tag := "v9.9.9"
		if r.URL.Path == "/repos/"+RepoAgent+"/releases" {
			tag = "v0.5.0"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Release{rel(tag, false)})
	}))
	defer srv.Close()

	db := testDB(t)
	mi := NewService(db, "1.0.0", true)
	mi.setBaseURL(srv.URL)
	ag := NewAgentService(db, true)
	ag.setBaseURL(srv.URL)

	if err := mi.Check(context.Background()); err != nil {
		t.Fatalf("miabi check: %v", err)
	}
	if err := ag.Check(context.Background()); err != nil {
		t.Fatalf("agent check: %v", err)
	}

	var rows []models.UpdateStatus
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d status rows, want one per component", len(rows))
	}
	if v := mi.LatestVersion(); v != "v9.9.9" {
		t.Errorf("miabi latest = %q, want v9.9.9", v)
	}
	if v := ag.LatestVersion(); v != "v0.5.0" {
		t.Errorf("agent latest = %q, want v0.5.0", v)
	}
}

// The agent check tracks no local build, so it must not disable itself the way a `dev` platform
// build does — every node reports its own version and none of them is this process's.
func TestAgentServiceEnabledWithoutALocalBuild(t *testing.T) {
	if !NewAgentService(nil, true).Enabled() {
		t.Error("the agent check disabled itself for having no local version")
	}
	if NewAgentService(nil, false).Enabled() {
		t.Error("the agent check ran while update checks are switched off")
	}
}

// legacyUpdateStatus is the table shape from before the agent had a check of its own: one unlabelled
// singleton row. It maps onto the same table as models.UpdateStatus on purpose.
type legacyUpdateStatus struct {
	ID            uint `gorm:"primaryKey"`
	LatestVersion string
	ReleaseURL    string
	ETag          string
}

func (legacyUpdateStatus) TableName() string { return "update_statuses" }

// An install that predates the agent check carries one row and no component column. AutoMigrate has
// to add a NOT NULL column to a populated table — illegal without a default — and the row it
// backfills is the platform's own. Getting this wrong fails at boot, so it is worth pinning.
func TestMigratingAnInstallThatPredatesComponents(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:uc_migrate_"+t.Name()+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&legacyUpdateStatus{}); err != nil {
		t.Fatalf("migrate legacy shape: %v", err)
	}
	if err := db.Create(&legacyUpdateStatus{ID: 1, LatestVersion: "v1.2.3", ETag: "W/\"abc\""}).Error; err != nil {
		t.Fatalf("seed the legacy row: %v", err)
	}

	if err := db.AutoMigrate(&models.UpdateStatus{}); err != nil {
		t.Fatalf("migrate to the component shape: %v", err)
	}

	var rows []models.UpdateStatus
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows after migrating, want the one that was there", len(rows))
	}
	if rows[0].Component != ComponentMiabi {
		t.Errorf("the pre-existing row came through as %q, want %q — the platform's own verdict was orphaned",
			rows[0].Component, ComponentMiabi)
	}
	if rows[0].LatestVersion != "v1.2.3" {
		t.Errorf("the migration lost the cached verdict: %q", rows[0].LatestVersion)
	}

	// And the agent's row is created beside it rather than colliding with it.
	if _, err := NewAgentService(db, true).Status(); err != nil {
		t.Fatalf("create the agent row alongside: %v", err)
	}
	var n int64
	if err := db.Model(&models.UpdateStatus{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("got %d rows, want one per component", n)
	}
}
