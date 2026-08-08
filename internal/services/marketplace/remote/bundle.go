// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package remote syncs the Miabi marketplace registry from the standalone marketplace service into a
// local cache, so the in-Miabi catalog can serve it alongside the embedded floor without a per-request
// round trip. It pulls the whole catalog ETag-conditionally and caches the bundle in Redis.
package remote

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

// Source labels carried through the bundle; they mirror the marketplace
// service's official/ vs community/ folders and drive the UI tabs/badges.
const (
	SourceOfficial  = "official"
	SourceCommunity = "community"
)

// Bundle is the marketplace service's GET /v1/export document: the entire
// catalog (both sources, all versions, manifests inline) in one JSON payload.
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
}

// BundleVersion is one immutable template version: the raw template.yaml plus
// its content digest for tamper verification.
type BundleVersion struct {
	Version  string `json:"version"`
	Digest   string `json:"digest"`   // sha256 of Manifest, "sha256:…" (manifest.Digest format)
	Manifest string `json:"manifest"` // raw template.yaml to install
}

// DecodedTemplate is a validated bundle template: its manifests parsed and
// digests verified, versions sorted newest-first.
type DecodedTemplate struct {
	Name     string
	Source   string
	Versions []DecodedVersion
}

// DecodedVersion pairs a version string with its parsed manifest.
type DecodedVersion struct {
	Version  string
	Manifest *manifest.Manifest
}

// decode parses a bundle JSON into validated templates. A version whose digest does not match its
// manifest, whose manifest fails to parse, or whose name disagrees with its template is dropped
// (tamper-evident, fail-closed); a template left with no valid versions is omitted entirely.
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
		dt := DecodedTemplate{Name: t.Name, Source: source}
		for _, v := range t.Versions {
			raw := []byte(v.Manifest)
			if v.Digest != "" && manifest.Digest(raw) != v.Digest {
				continue // tampered or corrupt — refuse
			}
			m, err := manifest.Parse(raw)
			if err != nil {
				continue
			}
			// Defend against a mislabeled bundle: the manifest's own name is
			// authoritative for install resolution, so a mismatch is dropped.
			if m.Metadata.Name != t.Name {
				continue
			}
			dt.Versions = append(dt.Versions, DecodedVersion{Version: m.Metadata.Version, Manifest: m})
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
