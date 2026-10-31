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
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// Version is a semantic version. Pre is the prerelease part without its leading dash.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

// ParseVersion reads MAJOR.MINOR.PATCH[-PRE], tolerating a leading "v".
func ParseVersion(s string) (Version, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%w: %q is not MAJOR.MINOR.PATCH", ErrInvalidVersion, s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
		}
		n[i] = v
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2], Pre: pre}, nil
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Core drops the prerelease part.
func (v Version) Core() Version { return Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch} }

// Compare orders versions by semver precedence, a prerelease sorting before its release.
func (v Version) Compare(o Version) int {
	for _, d := range [3]int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	return comparePre(v.Pre, o.Pre)
}

func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return sign(an - bn)
			}
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return sign(len(as) - len(bs))
}

func sign(d int) int {
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	}
	return 0
}

// Bump returns the next release version for a bump of the given part.
func (v Version) Bump(b models.VersionBump) Version {
	switch b {
	case models.BumpMajor:
		return Version{Major: v.Major + 1}
	case models.BumpMinor:
		return Version{Major: v.Major, Minor: v.Minor + 1}
	default:
		return Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1}
	}
}

// Series is a version line such as 1.x.x: its leading parts are fixed and the rest can grow.
type Series struct {
	raw   string
	fixed []int // the fixed leading parts, 0 to 2 of them
}

// ParseSeries reads a series pattern. Fixed parts must lead, and the patch is never fixed.
func ParseSeries(s string) (Series, error) {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Series{}, fmt.Errorf("%w: %q must have three parts, e.g. 1.x.x", ErrInvalidSeries, s)
	}
	var fixed []int
	open := false
	for _, p := range parts {
		if p == "x" || p == "*" {
			open = true
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || open {
			return Series{}, fmt.Errorf("%w: %q (fixed parts come first: 1.x.x, 1.4.x)", ErrInvalidSeries, s)
		}
		fixed = append(fixed, n)
	}
	if len(fixed) == 3 {
		return Series{}, fmt.Errorf("%w: %q leaves nothing to increase", ErrInvalidSeries, s)
	}
	return Series{raw: seriesString(fixed), fixed: fixed}, nil
}

func seriesString(fixed []int) string {
	parts := []string{"x", "x", "x"}
	for i, n := range fixed {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

func (s Series) String() string { return s.raw }

// Contains reports whether v (ignoring any prerelease) lies on the series.
func (s Series) Contains(v Version) bool {
	core := [3]int{v.Major, v.Minor, v.Patch}
	for i, n := range s.fixed {
		if core[i] != n {
			return false
		}
	}
	return true
}

// Allows reports whether a bump keeps the version on the series.
func (s Series) Allows(b models.VersionBump) bool {
	switch b {
	case models.BumpMajor:
		return len(s.fixed) == 0
	case models.BumpMinor:
		return len(s.fixed) <= 1
	}
	return true
}

// Clamp lowers a bump the series forbids to the largest one it allows.
func (s Series) Clamp(b models.VersionBump) models.VersionBump {
	for _, c := range []models.VersionBump{b, models.BumpMinor, models.BumpPatch} {
		if bumpRank(c) <= bumpRank(b) && s.Allows(c) {
			return c
		}
	}
	return models.BumpPatch
}

func bumpRank(b models.VersionBump) int {
	switch b {
	case models.BumpMajor:
		return 2
	case models.BumpMinor:
		return 1
	}
	return 0
}

// First is the opening version of a series that has none: start when it lies on the series, else the
// fixed parts followed by zeros (1.4.x → 1.4.0, 2.x.x → 2.0.0, x.x.x → 1.0.0).
func (s Series) First(start string) Version {
	if v, err := ParseVersion(start); err == nil && v.Pre == "" && s.Contains(v) {
		return v
	}
	var n [3]int
	copy(n[:], s.fixed)
	if len(s.fixed) == 0 {
		n[0] = 1
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2]}
}

// ValidBump reports whether b names a bump.
func ValidBump(b models.VersionBump) bool {
	return b == models.BumpPatch || b == models.BumpMinor || b == models.BumpMajor
}
