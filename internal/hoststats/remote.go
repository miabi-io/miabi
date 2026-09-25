// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package hoststats

import (
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/pkg/hoststats"
)

// SampleCommand is run inside a short-lived container on a node to produce one sample. A container's
// /proc/stat and /proc/meminfo are the HOST's — they are not namespaced — so this reads the node's
// real CPU and memory without binding anything. It is the fallback for nodes whose agent does not
// push stats (no agent at all, or an older one).
//
// The two cpu lines a second apart are the CPU sample window; the meminfo keys cover kernels with
// and without MemAvailable.
var SampleCommand = []string{
	"sh", "-c",
	"grep '^cpu ' /proc/stat; sleep 1; grep '^cpu ' /proc/stat; " +
		"grep -E '^(MemTotal|MemAvailable|MemFree|Buffers|Cached):' /proc/meminfo",
}

// SampleSeconds is how long SampleCommand takes, so callers can size their timeouts.
const SampleSeconds = 1

// ParseSample turns SampleCommand's output into a snapshot. It is deliberately a pure string
// function: the container is run by the caller, which owns the Docker client, so the parsing stays
// testable without one.
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
