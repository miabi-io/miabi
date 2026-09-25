// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import "testing"

func TestExecShell(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"", "/bin/sh", true},
		{"/bin/sh", "/bin/sh", true},
		{"/bin/bash", "/bin/bash", true},
		{"bash", "/bin/bash", true},
		{" sh ", "/bin/sh", true},
		{"/usr/bin/python3", "", false},
		{"python3", "", false},
		{"/bin/sh -c id", "", false},
		{"/bin/../usr/bin/python3", "", false},
		{"/bin/bash/", "", false},
	}
	for _, tc := range cases {
		got, ok := execShell(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("execShell(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
