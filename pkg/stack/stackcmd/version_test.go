// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package stackcmd

import "testing"

func TestNormalizeVersion(t *testing.T) {
	ok := map[string]string{
		"1.8.0":       "1.8.0",
		"v1.8.0":      "1.8.0",
		"V1.8.0":      "1.8.0",
		"  v1.8.0  ":  "1.8.0",
		"1.8.0-rc.1":  "1.8.0-rc.1",
		"v1.8.0-rc.1": "1.8.0-rc.1",
		"latest":      "latest",
		"vnext":       "vnext", // only a "v" followed by a digit is a version prefix
		"17-alpine":   "17-alpine",
		"v17-alpine":  "17-alpine",
	}
	for in, want := range ok {
		got, err := normalizeVersion(in)
		if err != nil {
			t.Errorf("normalizeVersion(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}

	// An image reference is a different flag. Accepting it here would silently produce
	// "miabi/miabi:miabi/miabi:1.8.0" and fail at the registry with a baffling message.
	for _, in := range []string{"miabi/miabi:1.8.0", "miabi:1.8.0", "ghcr.io/miabi/miabi", "miabi@sha256:abc"} {
		if _, err := normalizeVersion(in); err == nil {
			t.Errorf("normalizeVersion(%q): want an error pointing at --image, got none", in)
		}
	}
}

func TestRetag(t *testing.T) {
	cases := []struct{ ref, version, want string }{
		{"miabi/miabi:1.7.3", "1.8.0", "miabi/miabi:1.8.0"},
		{"miabi/miabi", "1.8.0", "miabi/miabi:1.8.0"}, // no tag to replace
		{"jkaninda/goma-gateway:0.13.1", "0.14.0", "jkaninda/goma-gateway:0.14.0"},
		{"postgres:17-alpine", "18-alpine", "postgres:18-alpine"},
		// A private registry must survive, which is the whole point of retagging rather than
		// rebuilding the reference from a hardcoded repo.
		{"registry.example.com/miabi:1.7.3", "1.8.0", "registry.example.com/miabi:1.8.0"},
		// The port is not a tag separator.
		{"registry.example.com:5000/miabi:1.7.3", "1.8.0", "registry.example.com:5000/miabi:1.8.0"},
		{"registry.example.com:5000/miabi", "1.8.0", "registry.example.com:5000/miabi:1.8.0"},
		// A digest pin has no tag; the requested version supersedes it.
		{"miabi/miabi@sha256:deadbeef", "1.8.0", "miabi/miabi:1.8.0"},
		{"registry.example.com:5000/ns/miabi@sha256:deadbeef", "1.8.0", "registry.example.com:5000/ns/miabi:1.8.0"},
	}
	for _, c := range cases {
		if got := retag(c.ref, c.version); got != c.want {
			t.Errorf("retag(%q, %q) = %q, want %q", c.ref, c.version, got, c.want)
		}
	}
}

func TestTagOf(t *testing.T) {
	cases := map[string]string{
		"miabi/miabi:1.8.0":                     "1.8.0",
		"miabi/miabi:latest":                    "latest",
		"miabi/miabi":                           "latest", // Docker resolves a bare repo to :latest
		"registry.example.com:5000/miabi":       "latest",
		"registry.example.com:5000/miabi:1.8.0": "1.8.0",
		"postgres:17-alpine":                    "17-alpine",
		"miabi/miabi@sha256:deadbeef":           "", // digest-pinned
	}
	for ref, want := range cases {
		if got := tagOf(ref); got != want {
			t.Errorf("tagOf(%q) = %q, want %q", ref, got, want)
		}
	}
}

// warnUI records whether a warning was emitted.
type warnUI struct{ warned bool }

func (w *warnUI) Printf(string, ...any)  {}
func (w *warnUI) Info(string, ...any)    {}
func (w *warnUI) Success(string, ...any) {}
func (w *warnUI) Warn(string, ...any)    { w.warned = true }
func (w *warnUI) Confirm(string) bool    { return true }

func TestWarnFloatingTag(t *testing.T) {
	floating := []string{
		"miabi/miabi:latest", "miabi/miabi", "miabi/miabi:edge", "miabi/miabi:main",
		"registry.example.com:5000/miabi:nightly",
	}
	for _, ref := range floating {
		u := &warnUI{}
		warnFloatingTag(u, ref)
		if !u.warned {
			t.Errorf("warnFloatingTag(%q): expected a warning", ref)
		}
	}
	pinned := []string{
		"miabi/miabi:1.8.0", "postgres:17-alpine", "jkaninda/goma-gateway:0.13.1",
		"miabi/miabi@sha256:deadbeef", "registry.example.com:5000/miabi:1.8.0",
	}
	for _, ref := range pinned {
		u := &warnUI{}
		warnFloatingTag(u, ref)
		if u.warned {
			t.Errorf("warnFloatingTag(%q): unexpected warning on a pinned reference", ref)
		}
	}
}
