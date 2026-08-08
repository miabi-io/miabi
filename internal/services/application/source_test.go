// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func uptr(v uint) *uint { return &v }

// gitApp is a fully populated git-source application: every field the git source owns is set, so a
// switch away from it has something to fail to clear.
func gitApp() *models.Application {
	return &models.Application{
		SourceType:      models.AppSourceGit,
		GitRepo:         "https://github.com/acme/api",
		GitRef:          "main",
		GitRepositoryID: uptr(7),
		BuildMethod:     models.BuildBuildpack,
		Builder:         "paketobuildpacks/builder-jammy-base",
		Buildpacks:      []string{"paketo/go"},
		BuildEnv:        map[string]string{"CGO_ENABLED": "0"},
	}
}

// imageApp is a fully populated image-source application.
func imageApp() *models.Application {
	return &models.Application{
		SourceType: models.AppSourceImage,
		Image:      "ghcr.io/acme/api",
		Tag:        "1.4.0",
		RegistryID: uptr(3),
	}
}

func TestApplySourceInput_GitToImageClearsGitFields(t *testing.T) {
	app := gitApp()
	err := applySourceInput(app, SourceInput{
		SourceType: models.AppSourceImage,
		Image:      "nginx",
		Tag:        "1.27",
		RegistryID: uptr(9),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app.SourceType != models.AppSourceImage {
		t.Errorf("source_type = %q, want image", app.SourceType)
	}
	if app.Image != "nginx" || app.Tag != "1.27" {
		t.Errorf("image/tag = %q/%q, want nginx/1.27", app.Image, app.Tag)
	}
	if app.RegistryID == nil || *app.RegistryID != 9 {
		t.Errorf("registry_id = %v, want 9", app.RegistryID)
	}
	// The whole point: nothing of the git source may survive.
	if app.GitRepo != "" || app.GitRef != "" || app.GitRepositoryID != nil {
		t.Errorf("git source not cleared: repo=%q ref=%q id=%v", app.GitRepo, app.GitRef, app.GitRepositoryID)
	}
	if app.BuildMethod != "" || app.Builder != "" || app.Buildpacks != nil || app.BuildEnv != nil {
		t.Errorf("build config not cleared: method=%q builder=%q bps=%v env=%v",
			app.BuildMethod, app.Builder, app.Buildpacks, app.BuildEnv)
	}
}

func TestApplySourceInput_ImageToGitClearsImageFields(t *testing.T) {
	app := imageApp()
	err := applySourceInput(app, SourceInput{
		SourceType:      models.AppSourceGit,
		GitRepo:         "  https://github.com/acme/web  ",
		GitRef:          "  develop  ",
		GitRepositoryID: uptr(4),
		BuildMethod:     models.BuildDockerfile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app.SourceType != models.AppSourceGit {
		t.Errorf("source_type = %q, want git", app.SourceType)
	}
	// Surrounding whitespace is a copy-paste artefact, not part of the URL.
	if app.GitRepo != "https://github.com/acme/web" || app.GitRef != "develop" {
		t.Errorf("git repo/ref = %q/%q, want trimmed values", app.GitRepo, app.GitRef)
	}
	// A git app's image comes from the build; a leftover pull target would be a lie.
	if app.Image != "" || app.Tag != "" || app.RegistryID != nil {
		t.Errorf("image source not cleared: image=%q tag=%q registry=%v", app.Image, app.Tag, app.RegistryID)
	}
}

func TestApplySourceInput_Validation(t *testing.T) {
	cases := []struct {
		name    string
		app     *models.Application
		in      SourceInput
		wantErr error
	}{
		{
			name:    "image without a reference",
			app:     gitApp(),
			in:      SourceInput{SourceType: models.AppSourceImage, Image: "   "},
			wantErr: ErrImageRequired,
		},
		{
			name:    "git without a repo or a saved repository",
			app:     imageApp(),
			in:      SourceInput{SourceType: models.AppSourceGit, GitRef: "main"},
			wantErr: ErrGitRepoRequired,
		},
		{
			name:    "unknown source type",
			app:     imageApp(),
			in:      SourceInput{SourceType: "helm"},
			wantErr: ErrSourceTypeInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := *tc.app
			if err := applySourceInput(tc.app, tc.in); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			// A rejected input must not half-apply: the app is the caller's live record.
			if tc.app.SourceType != before.SourceType {
				t.Errorf("source_type mutated on a rejected input: %q -> %q", before.SourceType, tc.app.SourceType)
			}
		})
	}
}

// A saved repository supplies the clone URL, so an empty git_repo is legal when one is attached.
func TestApplySourceInput_GitRepositoryIDSatisfiesTheURLRequirement(t *testing.T) {
	app := imageApp()
	if err := applySourceInput(app, SourceInput{
		SourceType:      models.AppSourceGit,
		GitRepositoryID: uptr(2),
	}); err != nil {
		t.Fatalf("a saved repository should satisfy the requirement: %v", err)
	}
}

func TestSourceChangeMessage(t *testing.T) {
	app := &models.Application{GitRepo: "https://github.com/acme/api", Image: "nginx"}
	cases := []struct {
		ch   *SourceChange
		want string
	}{
		{&SourceChange{To: models.AppSourceGit, Switched: false}, "Git source updated"},
		{&SourceChange{To: models.AppSourceImage, Switched: false}, "Image source updated"},
		{&SourceChange{To: models.AppSourceGit, Switched: true}, "Source switched from image to git (https://github.com/acme/api)"},
		{&SourceChange{To: models.AppSourceImage, Switched: true}, "Source switched from git to image (nginx)"},
	}
	for _, tc := range cases {
		if got := sourceChangeMessage(tc.ch, app); got != tc.want {
			t.Errorf("sourceChangeMessage(%+v) = %q, want %q", tc.ch, got, tc.want)
		}
	}
}
