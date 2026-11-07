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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/mwcatalog"
)

const committedDocs = "../../../docs/docs/middlewares"

func TestCommittedDocsAreCurrent(t *testing.T) {
	if _, err := os.Stat(committedDocs); err != nil {
		t.Skip("docs site not checked out beside this repository")
	}
	pages, err := Pages()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range pages {
		got, err := os.ReadFile(filepath.Join(committedDocs, name))
		if err != nil {
			t.Errorf("%s is missing — run `make mwdocs`", name)
			continue
		}
		if strings.TrimSpace(string(got)) != strings.TrimSpace(want) {
			t.Errorf("%s is stale — run `make mwdocs` and commit the result", name)
		}
	}
}

func TestNoOrphanedPages(t *testing.T) {
	if _, err := os.Stat(committedDocs); err != nil {
		t.Skip("docs site not checked out beside this repository")
	}
	pages, _ := Pages()
	entries, err := os.ReadDir(committedDocs)
	if err != nil {
		t.Fatal(err)
	}
	// overview.md is hand-written and deliberately not generated.
	allowed := map[string]bool{"overview.md": true}
	for name := range pages {
		allowed[name] = true
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if !allowed[e.Name()] {
			t.Errorf("%s documents a middleware the catalog no longer has — delete it", e.Name())
		}
	}
}

// A catalog type without a page is documented nowhere.
func TestEveryTypeIsDocumented(t *testing.T) {
	pages, err := Pages()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range mwcatalog.All() {
		if _, ok := pages[FileName(d.Type)]; !ok {
			t.Errorf("no page generated for %q", d.Type)
		}
	}
}

// The slug is the page URL, so it must stay kebab-case rather than leak the camelCase type.
func TestSlug(t *testing.T) {
	cases := map[string]string{
		"basicAuth":        "basic-auth",
		"oidc":             "oidc",
		"httpCache":        "http-cache",
		"errorInterceptor": "error-interceptor",
		"redirectRegex":    "redirect-regex",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// MDX reads `{` as an expression, so an unescaped template would fail the docs build.
func TestEscapeMDNeutralizesMDX(t *testing.T) {
	got := escapeMD(`use {{ .claim }} | and <tags>`)
	for _, unsafe := range []string{"{", "}", "<", ">"} {
		if strings.Contains(got, unsafe) {
			t.Errorf("escapeMD left %q in %q", unsafe, got)
		}
	}
	if !strings.Contains(got, `\|`) {
		t.Errorf("escapeMD did not escape the pipe: %q", got)
	}
}
