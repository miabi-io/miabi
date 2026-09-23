// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"errors"
	"strings"
	"testing"
)

// The finding: the browse API took a repository name from a query parameter and joined it under the
// caller's namespace, so a name carrying `..` addressed another workspace's repository. Every one of
// these must be refused before it reaches a URL.
func TestRepoPathRefusesTraversal(t *testing.T) {
	for _, name := range []string{
		"../ws_2/app",
		"..%2fws_2/app",
		"app/../../ws_2/app",
		"..",
		"../..",
		"./app",
		"app//other",  // empty component in the middle
		"",            //
		"   ",         //
		"ws_2\\app",   // backslash is not a component character
		"app:tag",     // a tag does not belong in a repository name
		"app?x=1",     // query injection
		"app#frag",    //
		"app%2f..%2f", //
		"App",         // distribution names are lowercase
		"-app",        // a separator must sit between alphanumerics
		"app-",        //
		".app",        //
		"app..other",  // the doubled dot the traversal needs, inside a component
	} {
		if _, err := repoPath(1, name); err == nil {
			t.Errorf("repoPath(%q) was accepted", name)
		} else if !errors.Is(err, ErrInvalidRepository) {
			t.Errorf("repoPath(%q) = %v, want ErrInvalidRepository", name, err)
		}
	}
}

// The names the platform and its users actually produce must still work — app handles are slugs, and
// a repository may be nested.
func TestRepoPathAcceptsRealNames(t *testing.T) {
	for name, want := range map[string]string{
		"app":            "ws_7/app",
		"my-app":         "ws_7/my-app",
		"my_app":         "ws_7/my_app",
		"my__app":        "ws_7/my__app",
		"app.v2":         "ws_7/app.v2",
		"team/app":       "ws_7/team/app",
		"a/b/c":          "ws_7/a/b/c",
		"app2":           "ws_7/app2",
		"/app/":          "ws_7/app", // the UI's surrounding slashes are trimmed, not honoured
		"  my-app  ":     "ws_7/my-app",
		"long-app-name1": "ws_7/long-app-name1",
	} {
		got, err := repoPath(7, name)
		if err != nil {
			t.Errorf("repoPath(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("repoPath(%q) = %q, want %q", name, got, want)
		}
	}
}

// The invariant that matters: whatever repoPath accepts addresses the CALLER's namespace. Leading
// and trailing slashes are trimmed rather than honoured, so a name that looks absolute resolves
// inside the workspace (and 404s there) instead of climbing out of it.
func TestRepoPathAlwaysStaysInTheNamespace(t *testing.T) {
	for _, name := range []string{
		"app", "team/app", "/ws_2/app", "//ws_2/app", "app/", "  app  ", "ws_2/app",
	} {
		got, err := repoPath(7, name)
		if err != nil {
			continue // refused is also fine; what must not happen is escaping the prefix
		}
		if !strings.HasPrefix(got, "ws_7/") {
			t.Errorf("repoPath(%q) = %q, which is outside the caller's namespace", name, got)
		}
		if strings.Contains(got, "..") {
			t.Errorf("repoPath(%q) = %q, which carries a dot segment", name, got)
		}
	}
}

// Distribution caps a whole repository name at 255 characters, namespace included.
func TestRepoPathEnforcesTheLengthLimit(t *testing.T) {
	if _, err := repoPath(1, strings.Repeat("a", maxRepositoryLength)); err == nil {
		t.Error("an over-long repository name was accepted")
	}
}

func TestValidateTag(t *testing.T) {
	for _, tag := range []string{"latest", "v1.2.3", "1", "_x", "a-b_c.d", strings.Repeat("a", 128)} {
		if err := validateTag(tag); err != nil {
			t.Errorf("validateTag(%q) = %v, want nil", tag, err)
		}
	}
	for _, tag := range []string{
		"", "..", "../x", "a/b", ".hidden", "-lead", "tag with space",
		strings.Repeat("a", 129), "tag%2f..", "tag?x",
	} {
		if err := validateTag(tag); err == nil {
			t.Errorf("validateTag(%q) was accepted", tag)
		}
	}
}

// Escaping is the second line, not the first: it keeps a stray slash from opening a path of its own.
// It deliberately does NOT neutralise `..` — url.PathEscape leaves a dot segment alone — which is
// why the grammar check runs on every caller-supplied name.
func TestEscapePath(t *testing.T) {
	for in, want := range map[string]string{
		"ws_1/app":     "ws_1/app",
		"ws_1/my-app":  "ws_1/my-app",
		"ws_1/a b":     "ws_1/a%20b",
		"ws_1/a?b":     "ws_1/a%3Fb",
		"ws_1/a#b":     "ws_1/a%23b",
		"ws_1/a/b/c":   "ws_1/a/b/c",
		"ws_1/x%2Fy":   "ws_1/x%252Fy",
		"ws_1/../ws_2": "ws_1/../ws_2", // unchanged, and refused upstream
	} {
		if got := escapePath(in); got != want {
			t.Errorf("escapePath(%q) = %q, want %q", in, got, want)
		}
	}
}
