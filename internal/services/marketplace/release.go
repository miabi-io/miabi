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
	"fmt"

	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
	"github.com/miabi-io/miabi/internal/services/marketplace/remote"
)

var (
	ErrTemplateDeprecated = errors.New("template is deprecated")
	ErrUpgradeBlocked     = errors.New("upgrade path blocked")
)

// ReleaseNote is one version's release notes in an upgrade plan.
type ReleaseNote struct {
	Version      string `json:"version"`
	Released     string `json:"released,omitempty"`
	Notes        string `json:"notes,omitempty"`
	Breaking     bool   `json:"breaking,omitempty"`
	RequiresFrom string `json:"requires_from,omitempty"`
}

// Only the synced registry carries release data; the embedded floor and custom imports have none.
func (s *Service) remoteTemplate(slug, source string) (remote.DecodedTemplate, bool) {
	if s.remote == nil || source == SourceCustom {
		return remote.DecodedTemplate{}, false
	}
	for _, t := range s.remote.Templates() {
		if t.Name == slug && (source == "" || t.Source == source) {
			return t, true
		}
	}
	return remote.DecodedTemplate{}, false
}

func (s *Service) releaseOf(slug, source, version string) remote.Release {
	t, ok := s.remoteTemplate(slug, source)
	if !ok {
		return remote.Release{}
	}
	for _, v := range t.Versions {
		if v.Version == version {
			return v.Release
		}
	}
	return remote.Release{}
}

// installableVersion is the newest version not deprecated; "" defers to the plain latest.
func installableVersion(t remote.DecodedTemplate) string {
	for _, v := range t.Versions {
		if !v.Release.Deprecated {
			return v.Version
		}
	}
	return ""
}

// decorate layers the registry's lifecycle data onto a catalog entry built from a manifest.
func (s *Service) decorate(e CatalogEntry) CatalogEntry {
	t, ok := s.remoteTemplate(e.Name, e.Source)
	if !ok {
		return e
	}
	if v := installableVersion(t); v != "" && v != e.Version {
		for _, dv := range t.Versions {
			if dv.Version == v && dv.Manifest != nil {
				e = entryFromManifest(dv.Manifest, e.Source, e.Versions)
			}
		}
	}
	e.Deprecated, e.DeprecationNote, e.ReplacedBy = t.Deprecated, t.DeprecationNote, t.ReplacedBy
	e.DeprecatedVersions = nil
	for _, v := range t.Versions {
		if v.Version == e.Version {
			e.Updated = v.Release.Released
		}
		if v.Release.Deprecated {
			e.DeprecatedVersions = append(e.DeprecatedVersions, v.Version)
		}
	}
	return e
}

// checkInstallable refuses a new install of a retired template or version; existing installs are unaffected.
func (s *Service) checkInstallable(slug, source, version string) error {
	t, ok := s.remoteTemplate(slug, source)
	if !ok {
		return nil
	}
	if t.Deprecated {
		return deprecatedErr(slug, t.DeprecationNote, t.ReplacedBy)
	}
	for _, v := range t.Versions {
		if v.Version == version && v.Release.Deprecated {
			return deprecatedErr(slug+" "+version, v.Release.DeprecationNote, "")
		}
	}
	return nil
}

func deprecatedErr(what, note, replacedBy string) error {
	msg := what + " is deprecated"
	if note != "" {
		msg += ": " + note
	}
	if replacedBy != "" {
		msg += fmt.Sprintf(" (use %q instead)", replacedBy)
	}
	return fmt.Errorf("%w: %s", ErrTemplateDeprecated, msg)
}

// upgradePath stops at the first version whose requiresFrom the install doesn't meet; reachable is the furthest version before it.
func upgradePath(t remote.DecodedTemplate, from, target string) (reachable string, blockedAt *remote.DecodedVersion) {
	reachable = from
	for i := len(t.Versions) - 1; i >= 0; i-- {
		v := t.Versions[i]
		if manifest.CompareVersions(v.Version, from) <= 0 || manifest.CompareVersions(v.Version, target) > 0 {
			continue
		}
		if rf := v.Release.RequiresFrom; rf != "" && manifest.CompareVersions(from, rf) < 0 {
			return reachable, &t.Versions[i]
		}
		if !v.Release.Deprecated {
			reachable = v.Version
		}
	}
	return reachable, nil
}

// releaseNotes lists the versions an upgrade crosses, oldest first, so the plan reads as a changelog.
func releaseNotes(t remote.DecodedTemplate, from, to string) []ReleaseNote {
	out := []ReleaseNote{}
	for i := len(t.Versions) - 1; i >= 0; i-- {
		v := t.Versions[i]
		if manifest.CompareVersions(v.Version, from) <= 0 || manifest.CompareVersions(v.Version, to) > 0 {
			continue
		}
		r := v.Release
		if r.Notes == "" && !r.Breaking && r.RequiresFrom == "" && r.Released == "" {
			continue
		}
		out = append(out, ReleaseNote{Version: v.Version, Released: r.Released, Notes: r.Notes, Breaking: r.Breaking, RequiresFrom: r.RequiresFrom})
	}
	return out
}
