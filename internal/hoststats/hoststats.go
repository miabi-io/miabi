// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hoststats reads real host CPU and memory usage from a procfs directory. /proc/stat and
// /proc/meminfo already reflect the host even from inside a container, so the process's own /proc
// works out of the box; binding the host's procfs to /host/proc is an explicit alternative.
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
	c1, err := readCPU(procPath)
	if err != nil {
		return Stats{}, err
	}
	select {
	case <-ctx.Done():
		return Stats{}, ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	c2, err := readCPU(procPath)
	if err != nil {
		return Stats{}, err
	}

	memTotal, memAvail, err := readMem(procPath)
	if err != nil {
		return Stats{}, err
	}
	used := uint64(0)
	if memTotal > memAvail {
		used = memTotal - memAvail
	}
	memPct := 0.0
	if memTotal > 0 {
		memPct = float64(used) / float64(memTotal) * 100
	}
	return Stats{
		CPUPercent:    cpuPercent(c1, c2),
		MemTotalBytes: memTotal,
		MemUsedBytes:  used,
		MemPercent:    memPct,
	}, nil
}

type cpuTimes struct {
	total uint64
	idle  uint64 // idle + iowait
}

func readCPU(procPath string) (cpuTimes, error) {
	f, err := os.Open(filepath.Join(procPath, "stat"))
	if err != nil {
		return cpuTimes{}, err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		// Fields: user nice system idle iowait irq softirq steal guest guest_nice
		fields := strings.Fields(line)[1:]
		var t cpuTimes
		for i, fld := range fields {
			v, perr := strconv.ParseUint(fld, 10, 64)
			if perr != nil {
				continue
			}
			t.total += v
			if i == 3 || i == 4 { // idle, iowait
				t.idle += v
			}
		}
		if t.total == 0 {
			return cpuTimes{}, fmt.Errorf("hoststats: empty cpu line")
		}
		return t, nil
	}
	if err := sc.Err(); err != nil {
		return cpuTimes{}, err
	}
	return cpuTimes{}, fmt.Errorf("hoststats: no cpu line in %s/stat", procPath)
}

func cpuPercent(a, b cpuTimes) float64 {
	totalDelta := float64(b.total) - float64(a.total)
	idleDelta := float64(b.idle) - float64(a.idle)
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

// readMem parses MemTotal and MemAvailable (bytes) from meminfo. Falls back to
// MemFree+Buffers+Cached for kernels without MemAvailable.
func readMem(procPath string) (total, avail uint64, err error) {
	f, err := os.Open(filepath.Join(procPath, "meminfo"))
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()

	vals := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, kb, ok := parseMemLine(sc.Text())
		if ok {
			vals[key] = kb * 1024 // kB -> bytes
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
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

func parseMemLine(line string) (key string, kb uint64, ok bool) {
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
