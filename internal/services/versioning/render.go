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
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// DefaultTemplate renders 1.4.2 as the tag v1.4.2.
const DefaultTemplate = "v{version}"

// dockerTag is the OCI distribution spec's tag grammar.
var dockerTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// tagVars are the values a tag template can name.
type tagVars struct {
	version, major, minor, patch, sha string
}

func semverVars(v Version, commit string) tagVars {
	return tagVars{
		version: v.String(), major: strconv.Itoa(v.Major), minor: strconv.Itoa(v.Minor),
		patch: strconv.Itoa(v.Patch), sha: shortSHA(commit),
	}
}

func shortSHA(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func render(tmpl string, v tagVars) string {
	return strings.NewReplacer(
		"{version}", v.version, "{major}", v.major, "{minor}", v.minor, "{patch}", v.patch, "{sha}", v.sha,
	).Replace(tmpl)
}

// RenderTag renders a template and checks the result is a legal image tag.
func RenderTag(tmpl string, v tagVars) (string, error) {
	tag := render(tmpl, v)
	if !dockerTag.MatchString(tag) {
		return "", fmt.Errorf("%w: %q", ErrInvalidTag, tag)
	}
	return tag, nil
}

// validateTemplate requires {version}, so every version renders a distinct tag.
func validateTemplate(tmpl string) error {
	if !strings.Contains(tmpl, "{version}") {
		return fmt.Errorf("%w: the template must contain {version} so each build gets its own tag", ErrInvalidTag)
	}
	_, err := RenderTag(tmpl, tagVars{version: "1.0.0-x.1", major: "1", minor: "0", patch: "0", sha: "abcdef0"})
	return err
}

// validateFloating refuses floating tags that could shadow a version or run tag, since those are immutable.
func validateFloating(tags []string) error {
	for _, t := range tags {
		switch {
		case strings.Contains(t, "{version}") || strings.Contains(t, "{patch}") || strings.Contains(t, "{sha}"):
			return fmt.Errorf("%w: floating tag %q would name a single build; use latest, v{major} or v{major}.{minor}", ErrInvalidTag, t)
		case strings.HasPrefix(t, "run-") || strings.HasPrefix(t, "cache-"):
			return fmt.Errorf("%w: floating tag %q is reserved", ErrInvalidTag, t)
		}
		if _, err := RenderTag(t, tagVars{major: "1", minor: "0"}); err != nil {
			return err
		}
	}
	return nil
}

// maxBranchPart caps the branch identifier inside a prerelease so the tag stays under 128 characters.
const maxBranchPart = 40

// prereleaseBranch maps a branch onto semver prerelease characters: feat/Login_UI → feat-login-ui.
func prereleaseBranch(branch string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(branch)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if len(s) > maxBranchPart {
		s = strings.TrimRight(s[:maxBranchPart], "-")
	}
	if s == "" {
		s = "branch"
	}
	return s
}

func branchMatches(patterns []string, branch string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, branch); ok {
			return true
		}
	}
	return false
}

// CalverFormats are the supported date parts of a calver version.
var CalverFormats = []string{"YYYY.MM", "YYYY.0M", "YY.MM", "YYYY.MM.DD"}

// DefaultCalverFormat is used when a calver policy names none.
const DefaultCalverFormat = "YYYY.MM"

func calverPrefix(format string, now time.Time) (string, error) {
	switch format {
	case "", "YYYY.MM":
		return fmt.Sprintf("%d.%d", now.Year(), int(now.Month())), nil
	case "YYYY.0M":
		return fmt.Sprintf("%d.%02d", now.Year(), int(now.Month())), nil
	case "YY.MM":
		return fmt.Sprintf("%d.%d", now.Year()%100, int(now.Month())), nil
	case "YYYY.MM.DD":
		return fmt.Sprintf("%d.%d.%d", now.Year(), int(now.Month()), now.Day()), nil
	}
	return "", fmt.Errorf("%w: calver format %q (use one of %s)", ErrInvalidSettings, format, strings.Join(CalverFormats, ", "))
}

// conventionalHeader matches a Conventional Commits subject: type(scope)!: description.
var conventionalHeader = regexp.MustCompile(`^([a-zA-Z]+)(\([^)]*\))?(!)?:`)

// BumpFromCommit derives a bump from a Conventional Commits message: a breaking change is major, feat
// is minor, anything else patch. ok is false when the message is not conventional.
func BumpFromCommit(msg string) (models.VersionBump, bool) {
	msg = strings.TrimSpace(msg)
	subject, body, _ := strings.Cut(msg, "\n")
	m := conventionalHeader.FindStringSubmatch(subject)
	if m == nil {
		return "", false
	}
	if m[3] == "!" || strings.Contains(body, "BREAKING CHANGE") || strings.Contains(body, "BREAKING-CHANGE") {
		return models.BumpMajor, true
	}
	if strings.EqualFold(m[1], "feat") {
		return models.BumpMinor, true
	}
	return models.BumpPatch, true
}
