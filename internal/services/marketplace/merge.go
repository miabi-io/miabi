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
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
	"github.com/miabi-io/miabi/internal/services/marketplace/remote"
)

// Community templates are untrusted and badged.
const SourceCommunity = remote.SourceCommunity

// nil when no marketplace URL is set: only the embedded official floor plus custom imports.
type RemoteCatalog interface {
	Templates() []remote.DecodedTemplate
	Manifest(slug, version string) (*manifest.Manifest, string, bool)
}

func (s *Service) SetRemote(r RemoteCatalog) { s.remote = r }

func (s *Service) ListPublic() []CatalogEntry { return s.officialAndCommunity() }

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

func (s *Service) GetPublicManifest(slug, version string) (*manifest.Manifest, bool) {
	m, _, ok := s.resolveOfficialOrCommunity(slug, version)
	return m, ok
}

type officialAgg struct {
	versions map[string]bool
	latest   *manifest.Manifest
}

// The registry is authoritative for official slugs so a corrected manifest goes live without a Miabi release.
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
		out = append(out, s.decorate(entryFromManifest(a.latest, SourceOfficial, sortedVersions(a.versions))))
	}
	for _, e := range community {
		out = append(out, s.decorate(e))
	}
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

// The registry overrides the built-in official copy; a community slug colliding with an official defers to it.
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
	// Official always trumps a colliding community slug.
	if embM, embOK := GetManifest(slug, version); embOK {
		return embM, SourceOfficial, true
	}
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
