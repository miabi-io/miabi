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

package workspace

import (
	"testing"

	"github.com/miabi-io/miabi/internal/slug"
)

func TestHandleGrammar(t *testing.T) {
	cases := map[string]string{
		"Acme Prod":        "acme-prod",
		"  Hello World!  ": "hello-world",
		"already-slug":     "already-slug",
		"Foo___Bar":        "foo-bar",
		"--edge--":         "edge",
	}
	for in, want := range cases {
		if got := slug.Make(in, ""); got != want {
			t.Errorf("slug.Make(%q) = %q, want %q", in, got, want)
		}
	}
}

// "system" is the system workspace's own handle and must be reserved for everyone else.
func TestReservedHandlesRejected(t *testing.T) {
	for _, h := range []string{"system", "admin", "api", "registry", "workspaces", "me"} {
		if !slug.IsReserved(h) {
			t.Errorf("expected %q to be reserved", h)
		}
	}
	if slug.IsReserved("acme") {
		t.Error("acme should not be reserved")
	}
}
