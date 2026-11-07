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

package hostmount

import "testing"

// Security core of host-path volumes: only a clean absolute subpath strictly under /mnt is accepted.
func TestValidateCustomHostPath(t *testing.T) {
	ok := []struct{ in, want string }{
		{"/mnt/nas/app", "/mnt/nas/app"},
		{"/mnt/nas/app/", "/mnt/nas/app"},
		{"/mnt/nas//app", "/mnt/nas/app"},
		{"  /mnt/data  ", "/mnt/data"},
		{"/mnt/a/b/../c", "/mnt/a/c"}, // internal .. that stays under /mnt is fine after clean
	}
	for _, tc := range ok {
		got, err := ValidateCustomHostPath(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("ValidateCustomHostPath(%q) = (%q, %v), want (%q, nil)", tc.in, got, err, tc.want)
		}
	}

	bad := []string{
		"",            // empty
		"/mnt",        // the root itself, not a subpath
		"/mnt/",       // cleans to /mnt
		"/mnt/..",     // escapes to /
		"/mnt/../etc", // traversal out of /mnt
		"/mntx/app",   // sibling prefix, not under /mnt/
		"/etc/passwd", // other tree
		"/var/run/x",  // other tree
		"mnt/app",     // relative
		"/",           // root
		"/mnt/a\x00b", // NUL byte
	}
	for _, in := range bad {
		if got, err := ValidateCustomHostPath(in); err == nil {
			t.Fatalf("ValidateCustomHostPath(%q) = (%q, nil), want an error", in, got)
		}
	}
}
