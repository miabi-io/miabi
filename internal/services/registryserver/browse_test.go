// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"context"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// The fake registry serves ws_7/{api,web} plus ws_8/secret; web has tags
// latest + 1.0 (digests sha256:wlatest / sha256:abc), api has v2 (sha256:av2).

func TestListRepositoriesPagePaginates(t *testing.T) {
	svc := listSvc(t)
	ctx := context.Background()

	first, total, err := svc.ListRepositoriesPage(ctx, 7, "", 0, 1, 0)
	if err != nil {
		t.Fatalf("page 0: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2 (ws_8 excluded)", total)
	}
	if len(first) != 1 || first[0].Name != "api" {
		t.Fatalf("page 0 = %+v, want [api]", first)
	}

	second, _, err := svc.ListRepositoriesPage(ctx, 7, "", 1, 1, 0)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(second) != 1 || second[0].Name != "web" {
		t.Fatalf("page 1 = %+v, want [web]", second)
	}

	// Past the end is an empty page, not an error — a client landing on a stale
	// page number must not see a failure.
	beyond, total, err := svc.ListRepositoriesPage(ctx, 7, "", 99, 20, 0)
	if err != nil || len(beyond) != 0 || total != 2 {
		t.Fatalf("beyond end = (%+v, %d, %v)", beyond, total, err)
	}
}

func TestListRepositoriesPageReportsFullTagCountWithPreview(t *testing.T) {
	repos, _, err := listSvc(t).ListRepositoriesPage(context.Background(), 7, "web", 0, 20, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("repos = %+v, want just web", repos)
	}
	// The preview is truncated; the count is not — that distinction is what lets
	// the list say "4 of 214" instead of lying about how many tags exist.
	if repos[0].TagCount != 2 {
		t.Errorf("TagCount = %d, want 2", repos[0].TagCount)
	}
	if len(repos[0].Tags) != 1 || repos[0].Tags[0] != "latest" {
		t.Errorf("preview = %v, want [latest]", repos[0].Tags)
	}
}

func TestListRepositoriesPageFiltersByName(t *testing.T) {
	repos, total, err := listSvc(t).ListRepositoriesPage(context.Background(), 7, "AP", 0, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Total must reflect the filter, or the pager renders pages that don't exist.
	if total != 1 || len(repos) != 1 || repos[0].Name != "api" {
		t.Fatalf("filtered = (%+v, %d), want [api] / 1", repos, total)
	}
}

func TestListTagsPageEnrichesFromTheCatalog(t *testing.T) {
	svc := listSvc(t)
	built := time.Unix(1700000000, 0).UTC()
	appID := uint(3)
	svc.SetCatalog(stubCatalog{
		digests: map[string]bool{"sha256:wlatest": true}, // latest is live
		rows: map[string]models.Image{
			"sha256:abc": {Digest: "sha256:abc", Commit: "7596d3b", BuiltAt: &built, ApplicationID: &appID},
		},
	})

	tags, total, err := svc.ListTagsPage(context.Background(), 7, "web", "", 0, 20)
	if err != nil {
		t.Fatalf("ListTagsPage: %v", err)
	}
	if total != 2 || len(tags) != 2 {
		t.Fatalf("tags = %+v, total = %d", tags, total)
	}
	// Display order: latest leads.
	if tags[0].Name != "latest" || tags[1].Name != "1.0" {
		t.Fatalf("order = %v", []string{tags[0].Name, tags[1].Name})
	}
	if tags[0].Digest != "sha256:wlatest" || !tags[0].InUse {
		t.Errorf("latest = %+v, want digest sha256:wlatest and in-use", tags[0])
	}
	if tags[0].SizeBytes != 3100 { // config 100 + layers 1000 + 2000
		t.Errorf("latest size = %d, want 3100", tags[0].SizeBytes)
	}
	if tags[1].InUse {
		t.Error("1.0 is not held by a release; it must not be marked in use")
	}
	if tags[1].Commit != "7596d3b" || tags[1].BuiltAt == nil || tags[1].ApplicationID == nil {
		t.Errorf("1.0 provenance not joined: %+v", tags[1])
	}
}

func TestListTagsPageFiltersAndPaginates(t *testing.T) {
	svc := listSvc(t)
	tags, total, err := svc.ListTagsPage(context.Background(), 7, "web", "1.", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(tags) != 1 || tags[0].Name != "1.0" {
		t.Fatalf("filtered = (%+v, %d), want [1.0] / 1", tags, total)
	}

	page2, total, err := svc.ListTagsPage(context.Background(), 7, "web", "", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(page2) != 1 || page2[0].Name != "1.0" {
		t.Fatalf("page 2 = (%+v, %d), want [1.0] / 2", page2, total)
	}
}

// A listing must survive a broken manifest: the tag is still returned, just
// without the details the manifest would have supplied.
func TestListTagsPageSurvivesUnreadableManifest(t *testing.T) {
	tags, _, err := listSvc(t).ListTagsPage(context.Background(), 7, "api", "", 0, 20)
	if err != nil {
		t.Fatalf("ListTagsPage: %v", err)
	}
	if len(tags) != 1 || tags[0].Name != "v2" {
		t.Fatalf("tags = %+v", tags)
	}
}

func TestOverview(t *testing.T) {
	ov, err := listSvc(t).Overview(context.Background(), 7, "web")
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if ov.Name != "web" || ov.TagCount != 2 {
		t.Fatalf("overview = %+v", ov)
	}
	if ov.LatestTag == nil || ov.LatestTag.Name != "latest" {
		t.Fatalf("latest tag = %+v, want latest", ov.LatestTag)
	}
	if ov.LatestTag.SizeBytes != 3100 {
		t.Errorf("latest size = %d, want 3100", ov.LatestTag.SizeBytes)
	}
}
