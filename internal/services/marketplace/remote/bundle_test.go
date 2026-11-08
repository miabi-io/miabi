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

package remote

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

func TestDecodeCarriesReleaseAndDeprecation(t *testing.T) {
	good := "sha256:" + strings.Repeat("a", 64)
	b := Bundle{Templates: []BundleTemplate{{
		Name: "cooltool", Source: SourceOfficial,
		Deprecation: Deprecation{Deprecated: true, ReplacedBy: "hottool"},
		Versions: []BundleVersion{{
			Version: "1.2.0", Digest: manifest.Digest([]byte(sampleManifest)), Manifest: sampleManifest,
			Release: &Release{Notes: "n", Breaking: true, RequiresFrom: "1.0.0", Images: map[string]string{"app": good, "bad": "latest"}},
		}},
	}}}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	tpls, err := decode(data)
	if err != nil || len(tpls) != 1 {
		t.Fatalf("decode: %v %d", err, len(tpls))
	}
	got := tpls[0]
	if !got.Deprecated || got.ReplacedBy != "hottool" {
		t.Fatalf("deprecation lost: %+v", got.Deprecation)
	}
	r := got.Versions[0].Release
	if !r.Breaking || r.RequiresFrom != "1.0.0" || r.Images["app"] != good {
		t.Fatalf("release lost: %+v", r)
	}
	if _, ok := r.Images["bad"]; ok {
		t.Fatal("a malformed digest pin must be dropped")
	}
}

// An older registry sends no release or deprecation fields; decoding must not need them.
func TestDecodeWithoutLifecycleFields(t *testing.T) {
	raw := `{"templates":[{"name":"cooltool","source":"official","versions":[{"version":"1.2.0","manifest":` +
		mustJSON(t, sampleManifest) + `}]}]}`
	tpls, err := decode([]byte(raw))
	if err != nil || len(tpls) != 1 || tpls[0].Deprecated || tpls[0].Versions[0].Release.Notes != "" {
		t.Fatalf("decode: %v %+v", err, tpls)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
