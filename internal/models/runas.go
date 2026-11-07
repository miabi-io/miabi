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

package models

import (
	"errors"
	"strconv"
	"strings"
)

// RunAsUser errors. Both are surfaced to the user, so they read as guidance.
var (
	ErrRunAsUserInvalid = errors.New(`run-as user must be "uid", "uid:gid", "name" or "name:group"`)
	ErrRunAsUserRoot    = errors.New("run-as user must be a non-root numeric uid under the restricted security profile")
)

// Linux caps a login name at 32 characters, and a uid never needs more.
const maxRunAsPart = 32

// Empty is valid and means the image's own user.
func NormalizeRunAsUser(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	user, group, hasGroup := strings.Cut(v, ":")
	if !runAsPartValid(user) || (hasGroup && !runAsPartValid(group)) {
		return "", ErrRunAsUserInvalid
	}
	return v, nil
}

func runAsPartValid(p string) bool {
	if p == "" || len(p) > maxRunAsPart {
		return false
	}
	for i, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		case (r == '-' || r == '.') && i > 0: // never leading, per useradd
		default:
			return false
		}
	}
	return true
}

// Only a numeric uid:gid qualifies since names resolve via the image's /etc/passwd; gid 0 is allowed (restricted profile).
func RunAsUserIsNonRoot(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	user, group, hasGroup := strings.Cut(v, ":")
	uid, err := strconv.Atoi(user)
	if err != nil || uid <= 0 {
		return false
	}
	if hasGroup {
		if gid, gerr := strconv.Atoi(group); gerr != nil || gid < 0 {
			return false
		}
	}
	return true
}
