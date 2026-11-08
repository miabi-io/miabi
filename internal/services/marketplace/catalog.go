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

// Package marketplace serves the template catalog and installs templates via the app, database, storage and stack services.
package marketplace

import (
	"fmt"
	"sort"
	"sync"

	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
	"github.com/miabi-io/miabi/templates"
	"gopkg.in/yaml.v3"
)

// Source labels distinguish where a catalog entry came from.
const (
	SourceOfficial = "official" // ships embedded with Miabi
	SourceCustom   = "custom"   // user-imported into a workspace
)

type CatalogEntry struct {
	Name         string           `json:"name"`         // unique template handle
	DisplayName  string           `json:"display_name"` // free-text catalog label
	Description  string           `json:"description"`
	Category     string           `json:"category"`
	Icon         string           `json:"icon,omitempty"`
	Tags         []string         `json:"tags,omitempty"`
	Homepage     string           `json:"homepage,omitempty"`
	Author       *manifest.Author `json:"author,omitempty"`
	Source       string           `json:"source"`
	Version      string           `json:"version"`          // latest
	Versions     []string         `json:"versions"`         // newest first
	Applications int              `json:"applications"`     // how many apps it deploys
	Databases    int              `json:"databases"`        // db dependencies
	Volumes      int              `json:"volumes"`          // managed volumes
	Configs      int              `json:"configs"`          // configuration files it ships
	DBOnly       bool             `json:"db_only"`          // provisions only a database
	Inputs       []manifest.Input `json:"inputs,omitempty"` // install-wizard questions
	// Lifecycle fields come from the synced registry; the embedded floor and custom imports leave them empty.
	Updated            string   `json:"updated,omitempty"`
	Deprecated         bool     `json:"deprecated,omitempty"`
	DeprecationNote    string   `json:"deprecation_note,omitempty"`
	ReplacedBy         string   `json:"replaced_by,omitempty"`
	DeprecatedVersions []string `json:"deprecated_versions,omitempty"`
}

type indexFile struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Name       string `yaml:"name"`
	Templates  []struct {
		Name     string `yaml:"name"`
		Versions []struct {
			Version string `yaml:"version"`
			Path    string `yaml:"path"`
			Digest  string `yaml:"digest"`
		} `yaml:"versions"`
	} `yaml:"templates"`
}

var (
	loadOnce  sync.Once
	loadErr   error
	manifests = map[string]*manifest.Manifest{} // key: slug@version
	latest    = map[string]string{}             // slug -> latest version
	entries   []CatalogEntry
)

func key(slug, version string) string { return slug + "@" + version }

// A digest mismatch or invalid manifest is a hard error, since the embedded content ships with the binary.
func load() {
	raw, err := templates.FS.ReadFile("index.yaml")
	if err != nil {
		loadErr = fmt.Errorf("read index.yaml: %w", err)
		return
	}
	var idx indexFile
	if err := yaml.Unmarshal(raw, &idx); err != nil {
		loadErr = fmt.Errorf("parse index.yaml: %w", err)
		return
	}
	for _, t := range idx.Templates {
		for _, v := range t.Versions {
			data, err := templates.FS.ReadFile(v.Path)
			if err != nil {
				loadErr = fmt.Errorf("read %s: %w", v.Path, err)
				return
			}
			if v.Digest != "" {
				if got := manifest.Digest(data); got != v.Digest {
					loadErr = fmt.Errorf("%s: digest mismatch (index %s, file %s)", v.Path, v.Digest, got)
					return
				}
			}
			m, err := manifest.Parse(data)
			if err != nil {
				loadErr = fmt.Errorf("%s: %w", v.Path, err)
				return
			}
			if m.Metadata.Name != t.Name || m.Metadata.Version != v.Version {
				loadErr = fmt.Errorf("%s: name/version (%s@%s) disagree with index (%s@%s)",
					v.Path, m.Metadata.Name, m.Metadata.Version, t.Name, v.Version)
				return
			}
			manifests[key(t.Name, v.Version)] = m
		}
		// versions are listed newest-first in the index; record the first as latest.
		if len(t.Versions) > 0 {
			latest[t.Name] = t.Versions[0].Version
		}
	}
	entries = buildEntries()
}

func buildEntries() []CatalogEntry {
	out := make([]CatalogEntry, 0, len(latest))
	for slug, lv := range latest {
		vers := []string{}
		for k := range manifests {
			if s, vv := splitKey(k); s == slug {
				vers = append(vers, vv)
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(vers)))
		out = append(out, entryFromManifest(manifests[key(slug, lv)], SourceOfficial, vers))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// versions is newest first; nil means just this one.
func entryFromManifest(m *manifest.Manifest, source string, versions []string) CatalogEntry {
	if versions == nil {
		versions = []string{m.Metadata.Version}
	}
	return CatalogEntry{
		Name: m.Metadata.Name, DisplayName: m.Metadata.DisplayName, Description: m.Metadata.Description,
		Category: m.Metadata.Category, Icon: m.Metadata.Icon, Tags: m.Metadata.Tags,
		Homepage: m.Metadata.Homepage, Author: m.Metadata.Author, Source: source,
		Version: m.Metadata.Version, Versions: versions,
		Applications: len(m.Applications), Databases: len(m.Databases), Volumes: len(m.Volumes),
		Configs: len(m.Configs),
		DBOnly:  m.IsDatabaseOnly(), Inputs: m.Inputs,
	}
}

func splitKey(k string) (slug, version string) {
	for i := 0; i < len(k); i++ {
		if k[i] == '@' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

// LoadError is exercised by the catalog test so a broken manifest fails CI.
func LoadError() error {
	loadOnce.Do(load)
	return loadErr
}

// List returns the catalog (latest version of each template), sorted by name.
func List() []CatalogEntry {
	loadOnce.Do(load)
	return entries
}

// GetEntry returns the listing entry for a slug.
func GetEntry(slug string) (CatalogEntry, bool) {
	loadOnce.Do(load)
	for _, e := range entries {
		if e.Name == slug {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// GetManifest returns a template manifest. An empty version selects the latest.
func GetManifest(slug, version string) (*manifest.Manifest, bool) {
	loadOnce.Do(load)
	if version == "" {
		version = latest[slug]
	}
	m, ok := manifests[key(slug, version)]
	return m, ok
}
