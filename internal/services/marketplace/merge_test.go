// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package marketplace

import (
	"fmt"
	"testing"

	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
	"github.com/miabi-io/miabi/internal/services/marketplace/remote"
)

type fakeRemote struct{ tpls []remote.DecodedTemplate }

func (f *fakeRemote) Templates() []remote.DecodedTemplate { return f.tpls }

func (f *fakeRemote) Manifest(slug, version string) (*manifest.Manifest, string, bool) {
	for _, t := range f.tpls {
		if t.Name != slug {
			continue
		}
		if version == "" {
			return t.Versions[0].Manifest, t.Source, true
		}
		for _, v := range t.Versions {
			if v.Version == version {
				return v.Manifest, t.Source, true
			}
		}
	}
	return nil, "", false
}

func parseManifest(t *testing.T, slug, name, version string) *manifest.Manifest {
	t.Helper()
	raw := fmt.Sprintf(`apiVersion: miabi.io/v1
kind: Template
metadata:
  name: %s
  displayName: %s
  version: %s
  category: Web
applications:
  - name: app
    image: %s
    tag: latest
    ports:
      - container: 8080
        scheme: http
`, slug, name, version, slug)
	m, err := manifest.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse %s@%s: %v", slug, version, err)
	}
	return m
}

func decoded(source, slug string, m *manifest.Manifest) remote.DecodedTemplate {
	return remote.DecodedTemplate{
		Name:     slug,
		Source:   source,
		Versions: []remote.DecodedVersion{{Version: m.Metadata.Version, Manifest: m}},
	}
}

func findEntry(entries []CatalogEntry, slug string) (CatalogEntry, bool) {
	for _, e := range entries {
		if e.Name == slug {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// TestMergeSyncedRegistry covers the merge outcomes: a synced official template
// overrides the embedded floor, and community templates surface as their own
// source.
func TestMergeSyncedRegistry(t *testing.T) {
	s := &Service{}
	s.SetRemote(&fakeRemote{tpls: []remote.DecodedTemplate{
		decoded(SourceOfficial, "nginx", parseManifest(t, "nginx", "Nginx", "2.0.0")),
		decoded(SourceCommunity, "mytool", parseManifest(t, "mytool", "My Tool", "0.3.0")),
	}})

	list := s.officialAndCommunity()

	// Synced official version newer than the embedded 1.0.0 wins as latest.
	nginx, ok := findEntry(list, "nginx")
	if !ok {
		t.Fatal("nginx missing from merged catalog")
	}
	if nginx.Source != SourceOfficial || nginx.Version != "2.0.0" {
		t.Fatalf("nginx not overridden by registry: source=%q version=%q", nginx.Source, nginx.Version)
	}

	// Community template surfaces with its own source label.
	tool, ok := findEntry(list, "mytool")
	if !ok || tool.Source != SourceCommunity {
		t.Fatalf("community template missing/mislabeled: %+v ok=%v", tool, ok)
	}

	// resolveManifest (install path) resolves the synced official version.
	m, src, ok := s.resolveManifest(0, "nginx", "")
	if !ok || src != SourceOfficial || m.Metadata.Version != "2.0.0" {
		t.Fatalf("resolveManifest nginx: src=%q version=%v ok=%v", src, m, ok)
	}
}

// TestMergeRemoteOverridesBuiltin asserts the registry is authoritative for
// official templates: its copy wins over the built-in floor even when its
// version is older (the floor is a vendored snapshot, not a version authority).
func TestMergeRemoteOverridesBuiltin(t *testing.T) {
	s := &Service{}
	s.SetRemote(&fakeRemote{tpls: []remote.DecodedTemplate{
		decoded(SourceOfficial, "nginx", parseManifest(t, "nginx", "Nginx", "0.9.0")),
	}})

	nginx, ok := findEntry(s.officialAndCommunity(), "nginx")
	if !ok {
		t.Fatal("nginx missing")
	}
	if nginx.Version != "0.9.0" {
		t.Fatalf("registry official should override the built-in floor, got %q", nginx.Version)
	}
	// The install path resolves the registry copy too.
	m, src, ok := s.resolveManifest(0, "nginx", "")
	if !ok || src != SourceOfficial || m.Metadata.Version != "0.9.0" {
		t.Fatalf("resolveManifest nginx: src=%q m=%v ok=%v", src, m, ok)
	}
}

// TestMergeFloorFillsSlugsAbsentFromRegistry asserts the embedded floor still
// serves slugs the registry does not deliver — the offline fallback.
func TestMergeFloorFillsSlugsAbsentFromRegistry(t *testing.T) {
	floor := List()
	if len(floor) == 0 {
		t.Skip("no embedded floor to test the fallback against")
	}
	want := floor[0].Name
	s := &Service{}
	// Registry carries only an unrelated official slug, not the floor's.
	s.SetRemote(&fakeRemote{tpls: []remote.DecodedTemplate{
		decoded(SourceOfficial, "registry-only", parseManifest(t, "registry-only", "Registry Only", "1.0.0")),
	}})
	if _, ok := findEntry(s.officialAndCommunity(), want); !ok {
		t.Fatalf("embedded slug %q absent from the registry should still appear (offline floor)", want)
	}
}

func TestMergeNoRemoteIsEmbeddedOnly(t *testing.T) {
	s := &Service{} // no remote configured
	merged := s.officialAndCommunity()
	embedded := List()
	if len(merged) != len(embedded) {
		t.Fatalf("without a registry the catalog should equal the embedded floor: merged=%d embedded=%d", len(merged), len(embedded))
	}
	for _, e := range merged {
		if e.Source != SourceOfficial {
			t.Fatalf("embedded-only entry should be official, got %q for %s", e.Source, e.Name)
		}
	}
}
