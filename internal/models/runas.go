// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// maxRunAsPart bounds each half of a run-as user. Linux caps a login name at 32
// characters, and a uid never needs more.
const maxRunAsPart = 32

// NormalizeRunAsUser trims a run-as user and validates its shape. The empty
// string is valid and means "run as the image's own user".
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

// runAsPartValid reports whether one half of a run-as user is a plausible
// account or group reference — a uid/gid, or a POSIX-ish name.
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

// RunAsUserIsNonRoot reports whether v pins the container to a non-root account that the platform can
// verify. Only a numeric uid qualifies: a name is resolved from the image's own /etc/passwd, which the
// workload controls, so "appuser" is free to be uid 0. A numeric gid is required for the same reason,
// but gid 0 is allowed — it is the arbitrary-uid convention the restricted profile itself uses.
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
