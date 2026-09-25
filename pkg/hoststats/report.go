// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package hoststats

import "errors"

// Report is what a node agent pushes to POST /api/v1/agent/stats. It carries no identity and no
// timestamp: the control plane takes the node from the bearer token and stamps receipt time itself.
type Report struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemTotalBytes uint64  `json:"mem_total_bytes"`
	MemUsedBytes  uint64  `json:"mem_used_bytes"`
	Load1         float64 `json:"load1"`
	UptimeSeconds uint64  `json:"uptime_s"`
}

// Stats converts the report into a snapshot.
func (r Report) Stats() Stats {
	pct := 0.0
	if r.MemTotalBytes > 0 {
		pct = float64(r.MemUsedBytes) / float64(r.MemTotalBytes) * 100
	}
	return Stats{CPUPercent: r.CPUPercent, MemTotalBytes: r.MemTotalBytes, MemUsedBytes: r.MemUsedBytes, MemPercent: pct}
}

// ErrNoBaseline is returned by Sampler.Sample until it has a previous CPU reading to measure against.
var ErrNoBaseline = errors.New("hoststats: no cpu baseline yet")

// Sampler measures CPU over the whole interval between successive calls rather than a short fixed
// window, so a bursty workload is averaged over what actually happened since the last report.
type Sampler struct {
	ProcPath string
	prev     CPUTimes
	have     bool
}

// Sample returns a report covering the time since the previous call. The first call, and any call
// after the counters went backwards, only records a baseline and returns ErrNoBaseline.
func (s *Sampler) Sample() (Report, error) {
	cur, err := ReadCPU(s.ProcPath)
	if err != nil {
		return Report{}, err
	}
	prev, had := s.prev, s.have
	s.prev, s.have = cur, true
	if !had || cur.Total <= prev.Total {
		return Report{}, ErrNoBaseline
	}
	total, avail, err := ReadMem(s.ProcPath)
	if err != nil {
		return Report{}, err
	}
	st := StatsFrom(prev, cur, total, avail)
	r := Report{CPUPercent: st.CPUPercent, MemTotalBytes: st.MemTotalBytes, MemUsedBytes: st.MemUsedBytes}
	// Extras: a host that hides them still reports CPU and memory.
	r.Load1, _ = ReadLoad1(s.ProcPath)
	r.UptimeSeconds, _ = ReadUptime(s.ProcPath)
	return r, nil
}
