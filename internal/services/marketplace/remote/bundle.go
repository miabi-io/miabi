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

// Package remote syncs the marketplace registry ETag-conditionally into a Redis cache.
package remote

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

const (
	SourceOfficial  = "official"
	SourceCommunity = "community"
)

type Bundle struct {
	ETag        string           `json:"etag"`
	GeneratedAt string           `json:"generatedAt"`
	Templates   []BundleTemplate `json:"templates"`
}

// BundleTemplate is one catalog entry with all of its published versions.
type BundleTemplate struct {
	Name     string          `json:"name"`
	Source   string          `json:"source"` // official | community
	Versions []BundleVersion `json:"versions"`
	Deprecation
}

// Deprecation retires a template: hidden from browsing and new installs, existing installs keep working.
type Deprecation struct {
	Deprecated      bool   `json:"deprecated,omitempty"`
	DeprecationNote string `json:"deprecation_note,omitempty"`
	ReplacedBy      string `json:"replaced_by,omitempty"`
}

type BundleVersion struct {
	Version  string   `json:"version"`
	Digest   string   `json:"digest"`   // sha256 of Manifest, "sha256:…" (manifest.Digest format)
	Manifest string   `json:"manifest"` // raw template.yaml to install
	Release  *Release `json:"release,omitempty"`
}

// Release is a version's release.yaml; the registry ships it outside the manifest so older Miabi versions ignore it.
type Release struct {
	Released string `json:"released,omitempty"`
	Notes    string `json:"notes,omitempty"`
	Breaking bool   `json:"breaking,omitempty"`
	// RequiresFrom is the oldest installed version allowed to upgrade straight to this one.
	RequiresFrom    string `json:"requires_from,omitempty"`
	Deprecated      bool   `json:"deprecated,omitempty"`
	DeprecationNote string `json:"deprecation_note,omitempty"`
	// Images pins a template app name to the digest its tag resolved to when the version was published.
	Images map[string]string `json:"images,omitempty"`
}

type DecodedTemplate struct {
	Name     string
	Source   string
	Versions []DecodedVersion
	Deprecation
}

type DecodedVersion struct {
	Version  string
	Manifest *manifest.Manifest
	Release  Release
}

// Fail-closed: versions with a digest mismatch, parse error or name mismatch are dropped.
func decode(data []byte) ([]DecodedTemplate, error) {
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("decode marketplace bundle: %w", err)
	}
	out := make([]DecodedTemplate, 0, len(b.Templates))
	for _, t := range b.Templates {
		source := SourceOfficial
		if t.Source == SourceCommunity {
			source = SourceCommunity
		}
		dt := DecodedTemplate{Name: t.Name, Source: source, Deprecation: t.Deprecation}
		for _, v := range t.Versions {
			raw := []byte(v.Manifest)
			if v.Digest != "" && manifest.Digest(raw) != v.Digest {
				continue // tampered or corrupt — refuse
			}
			m, err := manifest.Parse(raw)
			if err != nil {
				continue
			}
			// The manifest's own name is authoritative for install resolution, so a mislabeled bundle entry is dropped.
			if m.Metadata.Name != t.Name {
				continue
			}
			dv := DecodedVersion{Version: m.Metadata.Version, Manifest: m}
			if v.Release != nil {
				dv.Release = sanitizeRelease(*v.Release)
			}
			dt.Versions = append(dt.Versions, dv)
		}
		if len(dt.Versions) == 0 {
			continue
		}
		sort.SliceStable(dt.Versions, func(i, j int) bool {
			return manifest.CompareVersions(dt.Versions[i].Version, dt.Versions[j].Version) > 0
		})
		out = append(out, dt)
	}
	return out, nil
}

var digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// A malformed pin is dropped rather than failing the version, since an image without a pin still installs by tag.
func sanitizeRelease(r Release) Release {
	for app, d := range r.Images {
		if !digestRe.MatchString(d) {
			delete(r.Images, app)
		}
	}
	return r
}
