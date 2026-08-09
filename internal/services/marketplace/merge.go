// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package marketplace

import (
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
	"github.com/miabi-io/miabi/internal/services/marketplace/remote"
)

// SourceCommunity labels a template synced from the community/ folder of the
// marketplace registry (untrusted, badged). Official (embedded or synced) and
// custom (workspace import) are defined in catalog.go / marketplace.go.
const SourceCommunity = remote.SourceCommunity

// RemoteCatalog is the synced official+community catalog served by the standalone marketplace service,
// merged into the local one. nil when no marketplace URL is set, in which case the catalog is the
// embedded official floor plus workspace custom imports — exactly the pre-sync behavior.
type RemoteCatalog interface {
	// Templates returns the synced templates (official + community), versions
	// newest-first.
	Templates() []remote.DecodedTemplate
	// Manifest resolves a synced manifest (empty version = latest), reporting the
	// template's source label.
	Manifest(slug, version string) (*manifest.Manifest, string, bool)
}

// SetRemote wires the synced registry catalog. Optional: without it, only the
// embedded official floor and workspace custom imports are served.
func (s *Service) SetRemote(r RemoteCatalog) { s.remote = r }

// ListPublic returns the non-workspace catalog: the embedded official floor
// merged with the synced official+community registry (no custom imports). Backs
// the login-only global catalog endpoint.
func (s *Service) ListPublic() []CatalogEntry { return s.officialAndCommunity() }

// GetPublicEntry returns one entry from the public catalog, preferring official
// over a colliding community slug.
func (s *Service) GetPublicEntry(slug string) (CatalogEntry, bool) {
	rank := map[string]int{SourceOfficial: 2, SourceCommunity: 1}
	var best *CatalogEntry
	for _, e := range s.officialAndCommunity() {
		if e.Name != slug {
			continue
		}
		entry := e
		if best == nil || rank[entry.Source] > rank[best.Source] {
			best = &entry
		}
	}
	if best != nil {
		return *best, true
	}
	return CatalogEntry{}, false
}

// GetPublicManifest resolves a manifest from the public (official+community)
// catalog (empty version = latest).
func (s *Service) GetPublicManifest(slug, version string) (*manifest.Manifest, bool) {
	m, _, ok := s.resolveOfficialOrCommunity(slug, version)
	return m, ok
}

type officialAgg struct {
	versions map[string]bool
	latest   *manifest.Manifest
}

// officialAndCommunity returns the merged non-custom catalog. The synced registry is authoritative for
// official templates — a slug it delivers overrides the built-in copy entirely, so a corrected manifest goes
// live without a Miabi release. The embedded floor fills only slugs the registry did not deliver.
func (s *Service) officialAndCommunity() []CatalogEntry {
	officials := map[string]*officialAgg{}
	add := func(m *manifest.Manifest) {
		a := officials[m.Metadata.Name]
		if a == nil {
			a = &officialAgg{versions: map[string]bool{}}
			officials[m.Metadata.Name] = a
		}
		a.versions[m.Metadata.Version] = true
		if a.latest == nil || manifest.CompareVersions(m.Metadata.Version, a.latest.Metadata.Version) > 0 {
			a.latest = m
		}
	}

	// Synced registry first (authoritative). Record which official names it
	// covers so the built-in floor below only fills the gaps.
	remoteOfficial := map[string]bool{}
	var community []CatalogEntry
	if s.remote != nil {
		for _, t := range s.remote.Templates() {
			if t.Source == SourceCommunity {
				if e, ok := decodedEntry(t); ok {
					community = append(community, e)
				}
				continue
			}
			remoteOfficial[t.Name] = true
			for _, v := range t.Versions {
				if v.Manifest != nil {
					add(v.Manifest)
				}
			}
		}
	}

	// Built-in official floor: fills only names the registry did not deliver, so
	// a synced official template overrides its built-in copy.
	for _, e := range List() {
		if remoteOfficial[e.Name] {
			continue
		}
		for _, v := range e.Versions {
			if m, ok := GetManifest(e.Name, v); ok {
				add(m)
			}
		}
	}

	out := make([]CatalogEntry, 0, len(officials)+len(community))
	for _, a := range officials {
		out = append(out, entryFromManifest(a.latest, SourceOfficial, sortedVersions(a.versions)))
	}
	out = append(out, community...)
	sortByDisplayName(out)
	return out
}

func sortByDisplayName(entries []CatalogEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := strings.ToLower(entries[i].DisplayName), strings.ToLower(entries[j].DisplayName)
		if a != b {
			return a < b
		}
		if entries[i].Source != entries[j].Source {
			return entries[i].Source < entries[j].Source
		}
		return entries[i].Name < entries[j].Name
	})
}

// resolveOfficialOrCommunity finds the best non-custom manifest for a slug across the synced registry and
// the embedded floor. The registry is authoritative for official templates and overrides the built-in copy;
// the floor is the fallback. A community slug colliding with a built-in official defers to official.
func (s *Service) resolveOfficialOrCommunity(slug, version string) (*manifest.Manifest, string, bool) {
	var remM *manifest.Manifest
	var remSrc string
	var remOK bool
	if s.remote != nil {
		remM, remSrc, remOK = s.remote.Manifest(slug, version)
	}

	if remOK && remSrc == SourceOfficial {
		return remM, SourceOfficial, true
	}
	// Otherwise the built-in official floor serves (offline fallback, or a version
	// the registry dropped). Official always trumps a colliding community slug.
	if embM, embOK := GetManifest(slug, version); embOK {
		return embM, SourceOfficial, true
	}
	// Only the registry has it: a community slug, or an official version absent
	// from the floor.
	if remOK {
		return remM, remSrc, true
	}
	return nil, "", false
}

func decodedEntry(t remote.DecodedTemplate) (CatalogEntry, bool) {
	if len(t.Versions) == 0 || t.Versions[0].Manifest == nil {
		return CatalogEntry{}, false
	}
	vers := make([]string, 0, len(t.Versions))
	for _, v := range t.Versions {
		vers = append(vers, v.Version)
	}
	return entryFromManifest(t.Versions[0].Manifest, t.Source, vers), true
}

func sortedVersions(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return manifest.CompareVersions(out[i], out[j]) > 0 })
	return out
}
