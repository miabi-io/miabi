// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package slug derives URL/DNS-safe identifiers from human names.
package slug

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
)

var (
	invalid = regexp.MustCompile(`[^a-z0-9]+`)
	trim    = regexp.MustCompile(`(^-+|-+$)`)
)

// tokenAlphabet is lowercase alphanumeric — safe inside a DNS label.
const tokenAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// Token returns a random lowercase-alphanumeric string of length n, suitable as a DNS-safe segment
// of a container or host name. It falls back to a fixed filler only if the system RNG fails, which
// keeps callers total-length-stable.
func Token(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n)
	}
	for i := range b {
		b[i] = tokenAlphabet[int(b[i])%len(tokenAlphabet)]
	}
	return string(b)
}

// Make returns a slug for name, or fallback if it reduces to empty.
func Make(name, fallback string) string {
	s := trim.ReplaceAllString(invalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-"), "")
	if s == "" {
		return fallback
	}
	return s
}

// IsValid reports whether name is already a canonical slug — lowercase alphanumeric with single
// interior hyphens — i.e. Make is a no-op on it. Used to constrain user-chosen identifiers that
// become part of the gateway config, so they stay unique and Goma/DNS-safe.
func IsValid(name string) bool {
	return name != "" && Make(name, "") == name
}

// Unique returns a slug for name unique under exists (which reports whether a
// candidate is already taken).
func Unique(name, fallback string, exists func(string) (bool, error)) (string, error) {
	base := Make(name, fallback)
	candidate := base
	for i := 1; ; i++ {
		taken, err := exists(candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

// reserved is the shared namespace workspace names and usernames must avoid: global handles that
// would collide with literal API path segments, the registry routes, or the built-in system
// workspace. One list means the workspace and user validators enforce the same rules.
var reserved = map[string]bool{
	"system": true, "current": true, "me": true, "admin": true,
	"api": true, "v1": true, "internal": true, "registry": true,
	"login": true, "logout": true, "invitations": true, "new": true,
	"settings": true, "catalog": true,
	// Top-level API path segments that sit beside the workspace handle.
	"workspaces": true, "users": true, "auth": true, "health": true,
}

// IsReserved reports whether s is a reserved handle that may not be claimed as a
// workspace name or username. Comparison is on the canonical (slugified) form.
func IsReserved(s string) bool {
	return reserved[Make(s, "")]
}

// IsAvailableHandle reports whether s is a valid, non-reserved handle — i.e. it
// is already in canonical slug form (IsValid) and not in the reserved set. It is
// the shared gate for user-chosen workspace names and usernames.
func IsAvailableHandle(s string) bool {
	return IsValid(s) && !IsReserved(s)
}

// Available returns a free handle for name, or "" when name is blank, reduces to
// nothing, or the lookup fails — the caller then falls back to whatever it
// derives handles from.
func Available(name string, exists func(string) (bool, error)) string {
	if Make(name, "") == "" {
		return ""
	}
	handle, err := UniqueAvailable(name, "", exists)
	if err != nil {
		return ""
	}
	return handle
}

// UniqueAvailable returns a non-reserved slug for name, unique under exists. It
// is Unique with an extra suffix bump whenever a candidate lands on a reserved
// handle, so backfills and auto-derivation never produce a reserved handle.
func UniqueAvailable(name, fallback string, exists func(string) (bool, error)) (string, error) {
	base := Make(name, fallback)
	if base == "" {
		base = fallback
	}
	candidate := base
	for i := 1; ; i++ {
		if IsReserved(candidate) {
			candidate = fmt.Sprintf("%s-%d", base, i)
			continue
		}
		taken, err := exists(candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}
