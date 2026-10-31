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

package versioning

import (
	"fmt"
	"slices"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// Settings is a policy's configuration as the API and a pipeline's `versioning:` block write it. An
// empty field takes its default, so `versioning: {scheme: semver}` is a complete policy.
type Settings struct {
	Scheme       models.VersionScheme `json:"scheme" yaml:"scheme"`
	Template     string               `json:"template,omitempty" yaml:"template,omitempty"`
	Series       string               `json:"series,omitempty" yaml:"series,omitempty"`
	Bump         models.VersionBump   `json:"bump,omitempty" yaml:"bump,omitempty"`
	Start        string               `json:"start,omitempty" yaml:"start,omitempty"`
	CalverFormat string               `json:"calver_format,omitempty" yaml:"calver_format,omitempty"`
	Branches     BranchSettings       `json:"branches" yaml:"branches,omitempty"`
	FloatingTags []string             `json:"floating_tags,omitempty" yaml:"floating_tags,omitempty"`
	// BumpFromCommits reads Conventional Commits from the built commit: feat → minor, breaking → major.
	BumpFromCommits bool `json:"bump_from_commits,omitempty" yaml:"bump_from_commits,omitempty"`
}

// BranchSettings decides which branches release and how the others are tagged.
type BranchSettings struct {
	Release []string          `json:"release,omitempty" yaml:"release,omitempty"`
	Others  string            `json:"others,omitempty" yaml:"others,omitempty"`
	Series  map[string]string `json:"series,omitempty" yaml:"series,omitempty"`
}

// DefaultSettings opens at v1.0.0 and bumps the patch on every build.
func DefaultSettings() Settings { return Settings{Scheme: models.VersionSchemeSemver} }

// Normalize fills defaults and validates. The result is what a policy stores.
func (s Settings) Normalize() (Settings, error) {
	if s.Scheme == "" {
		s.Scheme = models.VersionSchemeSemver
	}
	s.Template = strings.TrimSpace(s.Template)
	if s.Template == "" {
		s.Template = DefaultTemplate
	}
	if s.Bump == "" {
		s.Bump = models.BumpPatch
	}
	if s.Branches.Others == "" {
		s.Branches.Others = models.OtherBranchesPrerelease
	}
	switch s.Scheme {
	case models.VersionSchemeSemver:
		if s.Series == "" {
			s.Series = DefaultSeries
		}
		series, err := ParseSeries(s.Series)
		if err != nil {
			return s, err
		}
		s.Series = series.String()
		if s.Start == "" {
			s.Start = series.First(DefaultStart).String()
		}
		start, err := ParseVersion(s.Start)
		if err != nil {
			return s, err
		}
		if start.Pre != "" || !series.Contains(start) {
			return s, fmt.Errorf("%w: start %s is not a release on %s", ErrInvalidSettings, s.Start, series)
		}
		s.Start = start.String()
		if !ValidBump(s.Bump) {
			return s, fmt.Errorf("%w: bump must be patch, minor or major", ErrInvalidSettings)
		}
		if !series.Allows(s.Bump) {
			return s, fmt.Errorf("%w: a %s bump is not possible on %s", ErrInvalidSettings, s.Bump, series)
		}
		for pattern, ser := range s.Branches.Series {
			parsed, err := ParseSeries(ser)
			if err != nil {
				return s, fmt.Errorf("branch %q: %w", pattern, err)
			}
			s.Branches.Series[pattern] = parsed.String()
		}
		s.CalverFormat = ""
	case models.VersionSchemeCalver:
		if s.CalverFormat == "" {
			s.CalverFormat = DefaultCalverFormat
		}
		if !slices.Contains(CalverFormats, s.CalverFormat) {
			return s, fmt.Errorf("%w: calver format %q (use one of %s)", ErrInvalidSettings, s.CalverFormat, strings.Join(CalverFormats, ", "))
		}
		if len(s.Branches.Series) > 0 {
			return s, fmt.Errorf("%w: branch series apply to semver only", ErrInvalidSettings)
		}
		s.Series, s.Start, s.BumpFromCommits = "", "", false
	case models.VersionSchemeRun:
		s = Settings{Scheme: models.VersionSchemeRun, Template: DefaultTemplate, Bump: models.BumpPatch,
			Branches: BranchSettings{Others: models.OtherBranchesPrerelease}}
		return s, nil
	default:
		return s, fmt.Errorf("%w: scheme must be semver, calver or run", ErrInvalidSettings)
	}
	if err := validateTemplate(s.Template); err != nil {
		return s, err
	}
	if err := validateFloating(s.FloatingTags); err != nil {
		return s, err
	}
	if s.Branches.Others != models.OtherBranchesPrerelease && s.Branches.Others != models.OtherBranchesRunTag {
		return s, fmt.Errorf("%w: branches.others must be prerelease or run-tag", ErrInvalidSettings)
	}
	for _, b := range s.Branches.Release {
		if strings.TrimSpace(b) == "" {
			return s, fmt.Errorf("%w: empty release branch", ErrInvalidSettings)
		}
	}
	return s, nil
}

// apply writes normalized settings onto a policy.
func (s Settings) apply(p *models.VersionPolicy) {
	p.Scheme, p.Template, p.Series, p.Bump, p.Start = s.Scheme, s.Template, s.Series, s.Bump, s.Start
	p.CalverFormat = s.CalverFormat
	p.ReleaseBranches, p.OtherBranches, p.BranchSeries = s.Branches.Release, s.Branches.Others, s.Branches.Series
	p.FloatingTags, p.BumpFromCommits = s.FloatingTags, s.BumpFromCommits
}

// SettingsOf reads a policy back as settings.
func SettingsOf(p *models.VersionPolicy) Settings {
	return Settings{
		Scheme: p.Scheme, Template: p.Template, Series: p.Series, Bump: p.Bump, Start: p.Start,
		CalverFormat: p.CalverFormat,
		Branches:     BranchSettings{Release: p.ReleaseBranches, Others: p.OtherBranches, Series: p.BranchSeries},
		FloatingTags: p.FloatingTags, BumpFromCommits: p.BumpFromCommits,
	}
}
