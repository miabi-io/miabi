// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"errors"
	"testing"
)

func TestValidateStorageClassPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"empty is the built-in class", "", "", true},
		{"a mounted disk", "/mnt/ssd1/miabi", "/mnt/ssd1/miabi", true},
		{"cleaned", "/mnt/ssd1//miabi/", "/mnt/ssd1/miabi", true},
		{"traversal is cleaned away, not followed", "/mnt/ssd1/../ssd2/data", "/mnt/ssd2/data", true},
		{"relative", "mnt/ssd1", "", false},
		{"root", "/", "", false},
		{"etc", "/etc", "", false},
		{"under etc", "/etc/miabi", "", false},
		{"the docker data root", "/var/lib/docker/volumes", "", false},
		{"traversal that lands on a system tree", "/mnt/../etc/shadow", "", false},
		{"a NUL byte", "/mnt/ssd1\x00/x", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateStorageClassPath(tc.in)
			if tc.ok {
				if err != nil {
					t.Fatalf("expected %q, got error %v", tc.want, err)
				}
				if got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
				return
			}
			if !errors.Is(err, ErrStorageClassPath) {
				t.Fatalf("expected ErrStorageClassPath, got %v (%q)", err, got)
			}
		})
	}
}

func TestStorageClassVolumePath(t *testing.T) {
	managed := &StorageClass{Name: "ssd-fast", Path: "/mnt/ssd1/miabi"}
	if got := managed.VolumePath("mb-vol-7-pgdata"); got != "/mnt/ssd1/miabi/mb-vol-7-pgdata" {
		t.Fatalf("managed class volume path = %q", got)
	}
	if !managed.Managed() {
		t.Fatal("a class with a path is managed")
	}

	// The built-in class has no path of its own: Docker decides where the volume lives, which is
	// what makes an install that registers no class behave exactly as it did before.
	builtin := &StorageClass{Name: DefaultStorageClassName}
	if builtin.Managed() {
		t.Fatal("the built-in class is not managed")
	}
	if got := builtin.VolumePath("mb-vol-7-pgdata"); got != "" {
		t.Fatalf("built-in class volume path = %q, want empty", got)
	}
}
