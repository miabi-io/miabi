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

package registryserver

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	// Names come from a query parameter, so `..` or extra slashes would escape the caller's namespace.
	ErrInvalidRepository = errors.New("invalid repository name")
	// ErrInvalidTag rejects a tag outside the distribution grammar, for the same reason.
	ErrInvalidTag = errors.New("invalid tag")
)

// Separators only between alphanumerics, so `..` cannot match, which closes traversal.
var repoComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*$`)

// A digest is accepted separately where meaningful.
var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

// maxRepositoryLength is the distribution limit on a whole repository name, namespace included.
const maxRepositoryLength = 255

// The grammar check leaves no spelling of `..`, absolute path or empty component that escapes the namespace.
func repoPath(workspaceID uint, image string) (string, error) {
	name, err := validateRepository(image)
	if err != nil {
		return "", err
	}
	repo := Namespace(workspaceID) + "/" + name
	if len(repo) > maxRepositoryLength {
		return "", fmt.Errorf("%w: %d characters, over the %d-character limit", ErrInvalidRepository, len(repo), maxRepositoryLength)
	}
	return repo, nil
}

// Trims surrounding slashes the UI may include.
func validateRepository(image string) (string, error) {
	name := strings.Trim(strings.TrimSpace(image), "/")
	if name == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidRepository)
	}
	for _, part := range strings.Split(name, "/") {
		if !repoComponent.MatchString(part) {
			return "", fmt.Errorf("%w: %q is not a valid path component", ErrInvalidRepository, part)
		}
	}
	return name, nil
}

// validateTag checks a tag before it is put in a URL path.
func validateTag(tag string) error {
	if !tagPattern.MatchString(strings.TrimSpace(tag)) {
		return fmt.Errorf("%w: %q", ErrInvalidTag, tag)
	}
	return nil
}

// Not the traversal guard: url.PathEscape leaves `..` intact, so the grammar check must run on every name.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
