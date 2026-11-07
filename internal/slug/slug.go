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

// The fixed filler on RNG failure keeps callers total-length-stable.
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

func IsValid(name string) bool {
	return name != "" && Make(name, "") == name
}

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

// One list so the workspace and user validators enforce the same rules.
var reserved = map[string]bool{
	"system": true, "current": true, "me": true, "admin": true,
	"api": true, "v1": true, "internal": true, "registry": true,
	"login": true, "logout": true, "invitations": true, "new": true,
	"settings": true, "catalog": true,
	"organizations": true, "orgs": true, "org": true,
	"workspaces": true, "users": true, "auth": true, "health": true,
}

func IsReserved(s string) bool {
	return reserved[Make(s, "")]
}

func IsAvailableHandle(s string) bool {
	return IsValid(s) && !IsReserved(s)
}

// Returns "" on blank input or a failed lookup; the caller then falls back to its own derivation.
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

// UniqueAvailable bumps past reserved handles too, so backfills never produce a reserved handle.
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
