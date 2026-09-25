// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

// Package hoststats reads real host CPU and memory usage from a procfs directory. /proc/stat and
// /proc/meminfo already reflect the host even from inside a container, so the process's own /proc
// works out of the box; binding the host's procfs to /host/proc is an explicit alternative.
//
// It is shared by the control plane and the node agent, so the figures both sides produce and the
// payload the agent pushes cannot drift apart.
package hoststats

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Stats is a point-in-time host resource snapshot.
type Stats struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemTotalBytes uint64  `json:"mem_total_bytes"`
	MemUsedBytes  uint64  `json:"mem_used_bytes"`
	MemPercent    float64 `json:"mem_percent"`
}

// Available reports whether procPath exposes a readable /proc/stat, i.e. host
// stats can be read from it.
func Available(procPath string) bool {
	_, err := os.Stat(filepath.Join(procPath, "stat"))
	return err == nil
}

// Read samples host CPU (over a short interval) and memory from procPath (e.g.
// "/proc" or a bound "/host/proc"). Returns an error if the procfs files can't
// be read or parsed.
func Read(ctx context.Context, procPath string) (Stats, error) {
	c1, err := ReadCPU(procPath)
	if err != nil {
		return Stats{}, err
	}
	select {
	case <-ctx.Done():
		return Stats{}, ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	c2, err := ReadCPU(procPath)
	if err != nil {
		return Stats{}, err
	}

	memTotal, memAvail, err := ReadMem(procPath)
	if err != nil {
		return Stats{}, err
	}
	return StatsFrom(c1, c2, memTotal, memAvail), nil
}

// StatsFrom assembles a snapshot from two CPU samples and a memory reading.
func StatsFrom(c1, c2 CPUTimes, memTotal, memAvail uint64) Stats {
	used := uint64(0)
	if memTotal > memAvail {
		used = memTotal - memAvail
	}
	memPct := 0.0
	if memTotal > 0 {
		memPct = float64(used) / float64(memTotal) * 100
	}
	return Stats{
		CPUPercent:    CPUPercent(c1, c2),
		MemTotalBytes: memTotal,
		MemUsedBytes:  used,
		MemPercent:    memPct,
	}
}

// CPUTimes is the aggregate "cpu" row of /proc/stat, in jiffies.
type CPUTimes struct {
	Total uint64
	Idle  uint64 // idle + iowait
}

// ReadCPU reads the aggregate CPU counters from procPath/stat.
func ReadCPU(procPath string) (CPUTimes, error) {
	f, err := os.Open(filepath.Join(procPath, "stat"))
	if err != nil {
		return CPUTimes{}, err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		return ParseCPULine(line)
	}
	if err := sc.Err(); err != nil {
		return CPUTimes{}, err
	}
	return CPUTimes{}, fmt.Errorf("hoststats: no cpu line in %s/stat", procPath)
}

// ParseCPULine reads the aggregate "cpu" row of /proc/stat. Shared with the remote sampler, which
// gets the same line from a helper container's stdout rather than from a file.
func ParseCPULine(line string) (CPUTimes, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return CPUTimes{}, fmt.Errorf("hoststats: malformed cpu line")
	}
	var t CPUTimes
	for i, fld := range fields[1:] {
		v, perr := strconv.ParseUint(fld, 10, 64)
		if perr != nil {
			continue
		}
		t.Total += v
		if i == 3 || i == 4 { // idle, iowait
			t.Idle += v
		}
	}
	if t.Total == 0 {
		return CPUTimes{}, fmt.Errorf("hoststats: empty cpu line")
	}
	return t, nil
}

// CPUPercent is the busy share of the interval between two samples, clamped to [0, 100]. A counter
// that went backwards reads as 0.
func CPUPercent(a, b CPUTimes) float64 {
	totalDelta := float64(b.Total) - float64(a.Total)
	idleDelta := float64(b.Idle) - float64(a.Idle)
	if totalDelta <= 0 {
		return 0
	}
	pct := (1 - idleDelta/totalDelta) * 100
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// ReadMem parses MemTotal and MemAvailable (bytes) from meminfo. Falls back to
// MemFree+Buffers+Cached for kernels without MemAvailable.
func ReadMem(procPath string) (total, avail uint64, err error) {
	f, err := os.Open(filepath.Join(procPath, "meminfo"))
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()

	vals := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, kb, ok := ParseMemLine(sc.Text())
		if ok {
			vals[key] = kb * 1024 // kB -> bytes
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	return MemFromVals(vals)
}

// MemFromVals resolves total and available memory from parsed meminfo keys (bytes). MemAvailable is
// preferred; the MemFree+Buffers+Cached sum is the fallback for kernels without it.
func MemFromVals(vals map[string]uint64) (total, avail uint64, err error) {
	total = vals["MemTotal"]
	if total == 0 {
		return 0, 0, fmt.Errorf("hoststats: MemTotal not found")
	}
	if a, ok := vals["MemAvailable"]; ok {
		avail = a
	} else {
		avail = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	return total, avail, nil
}

// ParseMemLine splits one meminfo row into its key and kB value.
func ParseMemLine(line string) (key string, kb uint64, ok bool) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return "", 0, false
	}
	fields := strings.Fields(line[colon+1:])
	if len(fields) == 0 {
		return "", 0, false
	}
	v, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return line[:colon], v, true
}

// ReadLoad1 reads the one-minute load average from procPath/loadavg.
func ReadLoad1(procPath string) (float64, error) {
	b, err := os.ReadFile(filepath.Join(procPath, "loadavg"))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, fmt.Errorf("hoststats: empty loadavg")
	}
	return strconv.ParseFloat(fields[0], 64)
}

// ReadUptime reads the host's uptime in whole seconds from procPath/uptime.
func ReadUptime(procPath string) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(procPath, "uptime"))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, fmt.Errorf("hoststats: empty uptime")
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return uint64(v), nil
}
