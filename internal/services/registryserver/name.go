// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// ErrInvalidRepository rejects a repository name that is not a name at all. The browse API takes
	// it from a query parameter and joins it under the caller's namespace, so a name carrying `..`
	// or its own slashes would address a path the caller was never scoped to.
	ErrInvalidRepository = errors.New("invalid repository name")
	// ErrInvalidTag rejects a tag outside the distribution grammar, for the same reason.
	ErrInvalidTag = errors.New("invalid tag")
)

// repoComponent is one path component of a repository name, as the OCI distribution spec defines it:
// lowercase alphanumerics, with single separators (`.`, `_`, `__`, `-`…) only BETWEEN them. `..` has
// no alphanumeric on either side, so it cannot match — which is what closes the traversal.
var repoComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*$`)

// tagPattern is the distribution grammar for a tag: a word character, then up to 127 more of word,
// dot or dash. A digest is accepted separately where one is meaningful.
var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

// maxRepositoryLength is the distribution limit on a whole repository name, namespace included.
const maxRepositoryLength = 255

// repoPath builds the registry path for one of a workspace's repositories and validates the result
// against the reference grammar. The namespace is Miabi's, so the check exists for `image`, which
// arrives from a URL: every component must be a name, which leaves no spelling of `..`, of an
// absolute path, or of an empty component that could climb out of the namespace.
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

// validateRepository checks the user-facing part of a repository name — everything after the
// workspace namespace — and returns it cleaned of the surrounding slashes the UI may include.
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

// escapePath escapes a registry path segment-by-segment, keeping the separators, so a stray slash
// or a byte that is not URL-safe cannot open a path of its own.
//
// It is NOT what stops traversal: url.PathEscape leaves `..` exactly as it found it, because a dot
// segment is legal in a URL path. Only the grammar check above refuses it, which is why that check
// runs on every caller-supplied name rather than being left to this.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
