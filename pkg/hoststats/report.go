// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package hoststats

import "errors"

// Report carries no identity or timestamp: the control plane takes the node from the token and stamps receipt.
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

// Sampler averages CPU over the whole interval since the last call, so bursty workloads are measured honestly.
type Sampler struct {
	ProcPath string
	prev     CPUTimes
	have     bool
}

// The first call, or one after counters went backwards, only records a baseline and returns ErrNoBaseline.
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
