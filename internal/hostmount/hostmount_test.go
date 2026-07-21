// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package hostmount

import "testing"

// TestValidateCustomHostPath is the security core of host-path volumes: only a
// clean absolute subpath strictly under /mnt is accepted; everything else (the
// root itself, traversal, other trees, relative/empty) is refused.
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
