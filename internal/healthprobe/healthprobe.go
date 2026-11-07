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

// Package healthprobe embeds the probe binary; without bin/ (make build-probe) builds fall back to a shell probe.
package healthprobe

import (
	"embed"
	"strings"
)

// Path is where the probe is placed inside a container.
const Path = "/.miabi/healthprobe"

//go:embed all:bin
var bins embed.FS

func Binary(arch string) ([]byte, bool) {
	goarch, ok := goArch(arch)
	if !ok {
		return nil, false
	}
	b, err := bins.ReadFile("bin/healthprobe-linux-" + goarch)
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

// Bundled reports whether this build carries a probe for any architecture.
func Bundled() bool {
	for _, arch := range []string{"amd64", "arm64"} {
		if _, ok := Binary(arch); ok {
			return true
		}
	}
	return false
}

func goArch(arch string) (string, bool) {
	switch strings.ToLower(arch) {
	case "x86_64", "amd64":
		return "amd64", true
	case "aarch64", "arm64":
		return "arm64", true
	}
	return "", false
}
