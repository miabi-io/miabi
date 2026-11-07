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

package hoststats

import (
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/pkg/hoststats"
)

// A container's /proc/stat and /proc/meminfo are not namespaced, so this reads the node's real usage.
var SampleCommand = []string{
	"sh", "-c",
	"grep '^cpu ' /proc/stat; sleep 1; grep '^cpu ' /proc/stat; " +
		"grep -E '^(MemTotal|MemAvailable|MemFree|Buffers|Cached):' /proc/meminfo",
}

// SampleSeconds is how long SampleCommand takes, so callers can size their timeouts.
const SampleSeconds = 1

// Pure string function so parsing stays testable without a Docker client.
func ParseSample(out string) (Stats, error) {
	var cpuLines []string
	vals := map[string]uint64{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "cpu "):
			cpuLines = append(cpuLines, line)
		case strings.Contains(line, ":"):
			if key, kb, ok := hoststats.ParseMemLine(line); ok {
				vals[key] = kb * 1024 // kB -> bytes
			}
		}
	}
	if len(cpuLines) < 2 {
		return Stats{}, fmt.Errorf("hoststats: sample has %d cpu lines, want 2", len(cpuLines))
	}
	c1, err := hoststats.ParseCPULine(cpuLines[0])
	if err != nil {
		return Stats{}, err
	}
	c2, err := hoststats.ParseCPULine(cpuLines[1])
	if err != nil {
		return Stats{}, err
	}
	memTotal, memAvail, err := hoststats.MemFromVals(vals)
	if err != nil {
		return Stats{}, err
	}
	return hoststats.StatsFrom(c1, c2, memTotal, memAvail), nil
}
