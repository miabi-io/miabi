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
	"strings"
	"testing"
)

// Regression: a `..` repository name addressed another workspace's repository.
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

// App handles are slugs, and a repository may be nested.
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

// Leading/trailing slashes are trimmed, so a name that looks absolute stays inside the workspace.
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

// Escaping deliberately does not neutralise `..`, which is why the grammar check runs on every name.
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
