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

package marketplace

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/marketplace/remote"
)

func versioned(t *testing.T, slug string, rels map[string]remote.Release, versions ...string) remote.DecodedTemplate {
	t.Helper()
	dt := remote.DecodedTemplate{Name: slug, Source: SourceOfficial}
	for _, v := range versions {
		dt.Versions = append(dt.Versions, remote.DecodedVersion{Version: v, Manifest: parseManifest(t, slug, slug, v), Release: rels[v]})
	}
	return dt
}

func TestUpgradePathStopsBeforeRequiresFrom(t *testing.T) {
	tpl := versioned(t, "nc", map[string]remote.Release{
		"3.0.0": {RequiresFrom: "2.0.0"},
	}, "3.0.0", "2.0.0", "1.1.0", "1.0.0")

	reach, blocked := upgradePath(tpl, "1.0.0", "3.0.0")
	if reach != "2.0.0" || blocked == nil || blocked.Version != "3.0.0" {
		t.Fatalf("from 1.0.0: reach=%s blocked=%v", reach, blocked)
	}
	if reach, blocked := upgradePath(tpl, "2.0.0", "3.0.0"); reach != "3.0.0" || blocked != nil {
		t.Fatalf("from 2.0.0: reach=%s blocked=%v", reach, blocked)
	}
}

func TestUpgradePathSkipsDeprecatedVersions(t *testing.T) {
	tpl := versioned(t, "app", map[string]remote.Release{"1.2.0": {Deprecated: true}}, "1.2.0", "1.1.0", "1.0.0")
	if reach, _ := upgradePath(tpl, "1.0.0", "1.2.0"); reach != "1.1.0" {
		t.Fatalf("reach = %s", reach)
	}
	if v := installableVersion(tpl); v != "1.1.0" {
		t.Fatalf("installable = %s", v)
	}
}

func TestReleaseNotesCoverTheCrossedRange(t *testing.T) {
	tpl := versioned(t, "app", map[string]remote.Release{
		"1.1.0": {Notes: "one"},
		"1.2.0": {Notes: "two", Breaking: true},
		"1.3.0": {Notes: "three"},
	}, "1.3.0", "1.2.0", "1.1.0", "1.0.0")
	notes := releaseNotes(tpl, "1.0.0", "1.2.0")
	if len(notes) != 2 || notes[0].Version != "1.1.0" || notes[1].Version != "1.2.0" || !notes[1].Breaking {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestDecorateAndInstallability(t *testing.T) {
	old := versioned(t, "old", nil, "1.0.0")
	old.Deprecation = remote.Deprecation{Deprecated: true, DeprecationNote: "gone upstream", ReplacedBy: "new"}
	app := versioned(t, "app", map[string]remote.Release{
		"2.0.0": {Deprecated: true},
		"1.0.0": {Released: "2026-11-01"},
	}, "2.0.0", "1.0.0")
	s := &Service{}
	s.SetRemote(&fakeRemote{tpls: []remote.DecodedTemplate{old, app}})

	e := s.decorate(entryFromManifest(app.Versions[0].Manifest, SourceOfficial, []string{"2.0.0", "1.0.0"}))
	if e.Version != "1.0.0" || e.Updated != "2026-11-01" || len(e.DeprecatedVersions) != 1 {
		t.Fatalf("entry = %+v", e)
	}
	err := s.checkInstallable("old", SourceOfficial, "1.0.0")
	if !errors.Is(err, ErrTemplateDeprecated) || !strings.Contains(err.Error(), `"new"`) {
		t.Fatalf("deprecated template: %v", err)
	}
	if err := s.checkInstallable("app", SourceOfficial, "2.0.0"); !errors.Is(err, ErrTemplateDeprecated) {
		t.Fatalf("deprecated version: %v", err)
	}
	if err := s.checkInstallable("app", SourceOfficial, "1.0.0"); err != nil {
		t.Fatalf("installable version refused: %v", err)
	}
	if err := s.checkInstallable("old", SourceCustom, "1.0.0"); err != nil {
		t.Fatalf("a custom import never inherits registry deprecation: %v", err)
	}
}

func TestSetPin(t *testing.T) {
	const a, b = "sha256:aaaa", "sha256:bbbb"
	cases := []struct {
		name         string
		cur, pin     string
		imageChanged bool
		want         string
		redeploy     bool
	}{
		{"new pin with new image", "", a, true, a, false},
		{"pin added to running image", "", a, false, a, false},
		{"rebuilt upstream", a, b, false, b, true},
		{"new image without a pin clears the stale one", a, "", true, "", false},
		{"no pin, same image keeps the old one", a, "", false, a, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := &models.Application{Metadata: models.Metadata{}}
			if c.cur != "" {
				app.Metadata = models.SetBuiltin(app.Metadata, models.MetaDigest, c.cur)
			}
			if got := setPin(app, c.pin, c.imageChanged); got != c.redeploy {
				t.Fatalf("redeploy = %v", got)
			}
			if got := app.Metadata[models.MetaDigest]; got != c.want {
				t.Fatalf("pin = %q, want %q", got, c.want)
			}
		})
	}
}
