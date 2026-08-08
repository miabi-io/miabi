// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// Upgrade paths: how the data is carried from the old version to the new one.
const (
	PathInPlace     = "in-place"     // swap the image on the same data volume
	PathDumpRestore = "dump-restore" // dump all logical DBs and restore into a fresh volume
)

// versionComponents splits a version string into its leading numeric components,
// ignoring any non-numeric suffix (e.g. "17.2-alpine" -> [17, 2], "8.4" -> [8, 4],
// "16-alpine" -> [16]). Unparseable input yields an empty slice.
func versionComponents(v string) []int {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	// Cut a tag suffix like "-alpine" / "-bookworm" before parsing.
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, part := range strings.Split(v, ".") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			break // stop at the first non-numeric component
		}
		out = append(out, n)
	}
	return out
}

// majorOf returns the major version number (the first numeric component), and
// whether it could be parsed.
func majorOf(v string) (int, bool) {
	c := versionComponents(v)
	if len(c) == 0 {
		return 0, false
	}
	return c[0], true
}

// cmpVersion compares two version strings by their numeric components.
// Returns -1 if a<b, 0 if equal, +1 if a>b. A shorter prefix that matches is
// treated as smaller (16 < 16.2). Unparseable versions compare as equal.
func cmpVersion(a, b string) int {
	ca, cb := versionComponents(a), versionComponents(b)
	for i := 0; i < len(ca) || i < len(cb); i++ {
		var x, y int
		if i < len(ca) {
			x = ca[i]
		}
		if i < len(cb) {
			y = cb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// upgradePath decides how to move engine e from version `from` to `to`: the same major (or Redis, which
// reads its persistence forward) swaps the image on the same volume, while a SQL major bump needs a dump
// and restore into a fresh volume. Returns the path, whether it crosses a major, and an error for non-upgrades.
func upgradePath(e models.DBEngine, from, to string) (path string, major bool, err error) {
	if strings.TrimSpace(to) == "" {
		return "", false, ErrInvalidVersion
	}
	// libSQL stores data as SQLite (a stable, forward-compatible file format), so any version change is an
	// in-place image swap on the same volume — like Redis. Its image uses non-numeric tags (e.g. "latest"), so
	// skip the numeric-major gating the other engines rely on. Re-pinning the same tag is a no-op.
	if e == models.DBEngineLibSQL {
		if strings.TrimSpace(from) == strings.TrimSpace(to) {
			return "", false, ErrAlreadyOnVersion
		}
		return PathInPlace, false, nil
	}
	if _, ok := majorOf(to); !ok {
		return "", false, ErrInvalidVersion
	}
	switch c := cmpVersion(from, to); {
	case c == 0:
		return "", false, ErrAlreadyOnVersion
	case c > 0:
		return "", false, ErrDowngrade
	}
	fromMajor, okF := majorOf(from)
	toMajor, _ := majorOf(to)
	major = !okF || toMajor != fromMajor
	// MongoDB majors must be applied sequentially and gate on
	// featureCompatibilityVersion, so a dump/restore swap isn't safe. Allow
	// in-place minor upgrades only; reject major bumps until guided upgrades land.
	if e == models.DBEngineMongoDB && major {
		return "", true, ErrMongoMajorUpgrade
	}
	// Redis keeps its RDB/AOF readable across upgrades, so it never needs a copy;
	// MongoDB minor upgrades are likewise in-place on the same data files.
	if e == models.DBEngineRedis || e == models.DBEngineMongoDB || !major {
		return PathInPlace, major, nil
	}
	return PathDumpRestore, major, nil
}

// suggestedVersions returns a curated list of common upgrade targets newer than
// `current` for the engine, for the UI picker. Users may still type any version;
// these are only hints (Miabi has no live engine-version catalog).
func suggestedVersions(e models.DBEngine, current string) []string {
	all := map[models.DBEngine][]string{
		models.DBEnginePostgres: {"14", "15", "16", "17", "18"},
		models.DBEngineMySQL:    {"8.0", "8.4", "9.0"},
		models.DBEngineMariaDB:  {"10.6", "10.11", "11", "11.4"},
		models.DBEngineRedis:    {"6", "7", "8"},
		models.DBEngineMongoDB:  {"6.0", "7.0", "8.0"},
		models.DBEngineLibSQL:   {"latest"},
	}
	out := []string{}
	for _, v := range all[e] {
		if cmpVersion(current, v) < 0 {
			out = append(out, v)
		}
	}
	return out
}
