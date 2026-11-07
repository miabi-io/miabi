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

package nodestats

import (
	"errors"
	"math"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/pkg/hoststats"
)

const (
	// pushStaleAfter is three missed 15s pushes: past it the node falls back to a container sample.
	pushStaleAfter = 90 * time.Second
	// Bounds server writes to about one per node per minute despite 15s pushes.
	persistEvery = time.Minute
	// Movement that is written straight away rather than waiting out persistEvery.
	usageDriftCPUPoints = 10.0
	usageDriftMemPct    = 5
)

// Rejected rather than clamped so a broken agent shows as unmeasured instead of idle.
var ErrInvalidReport = errors.New("invalid host stats report")

// SetAgentStats toggles agent-pushed readings (MIABI_NODE_STATS_AGENT); off, remote nodes are sampled by container.
func (s *Service) SetAgentStats(on bool) {
	s.mu.Lock()
	s.agentOn = on
	s.mu.Unlock()
}

// ValidateReport rejects a report that cannot describe a real host.
func ValidateReport(r hoststats.Report) error {
	switch {
	case math.IsNaN(r.CPUPercent) || r.CPUPercent < 0 || r.CPUPercent > 100:
		return errors.Join(ErrInvalidReport, errors.New("cpu_percent must be within [0, 100]"))
	case r.MemTotalBytes == 0:
		return errors.Join(ErrInvalidReport, errors.New("mem_total_bytes must be positive"))
	case r.MemUsedBytes > r.MemTotalBytes:
		return errors.Join(ErrInvalidReport, errors.New("mem_used_bytes exceeds mem_total_bytes"))
	case math.IsNaN(r.Load1) || r.Load1 < 0:
		return errors.Join(ErrInvalidReport, errors.New("load1 must be non-negative"))
	}
	return nil
}

// Ingest writes usage columns at most once per persistEvery, or sooner on real movement.
func (s *Service) Ingest(srv *models.Server, r hoststats.Report) error {
	if err := ValidateReport(r); err != nil {
		return err
	}
	s.mu.Lock()
	if !s.agentOn {
		s.mu.Unlock()
		return nil
	}
	now := s.now()

	sample := Sample{
		Stats:             r.Stats(),
		DescribesNode:     describesNode(r.MemTotalBytes, srv.MemoryBytes),
		NodeMemTotalBytes: srv.MemoryBytes,
		Source:            SourceAgent,
		MeasuredAt:        now,
		Load1:             r.Load1,
		UptimeSeconds:     r.UptimeSeconds,
	}
	s.pushed[srv.ID] = sample
	persist := sample.DescribesNode && s.servers != nil && usageWorthWriting(s.persisted[srv.ID], sample)
	if persist {
		s.persisted[srv.ID] = sample
	}
	s.mu.Unlock()

	if !persist {
		return nil
	}
	used := int64(sample.MemUsedBytes)
	if srv.MemoryBytes > 0 && used > srv.MemoryBytes {
		used = srv.MemoryBytes
	}
	if err := s.servers.SetUsage(srv.ID, sample.CPUPercent, used, now); err != nil {
		logger.Warn("node stats: record pushed usage failed", "node", srv.Name, "error", err)
	}
	return nil
}

// HasFreshPush reports whether serverID's agent has pushed recently enough to be relied on.
func (s *Service) HasFreshPush(serverID uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.freshPushLocked(serverID)
	return ok
}

func (s *Service) freshPushLocked(serverID uint) (Sample, bool) {
	if !s.agentOn {
		return Sample{}, false
	}
	p, ok := s.pushed[serverID]
	if !ok || s.now().Sub(p.MeasuredAt) >= pushStaleAfter {
		return Sample{}, false
	}
	return p, true
}

func usageWorthWriting(last, cur Sample) bool {
	if last.MeasuredAt.IsZero() || cur.MeasuredAt.Sub(last.MeasuredAt) >= persistEvery {
		return true
	}
	if math.Abs(cur.CPUPercent-last.CPUPercent) >= usageDriftCPUPoints {
		return true
	}
	if last.MemUsedBytes == 0 {
		return cur.MemUsedBytes != 0
	}
	delta := int64(cur.MemUsedBytes) - int64(last.MemUsedBytes)
	if delta < 0 {
		delta = -delta
	}
	return delta*100/int64(last.MemUsedBytes) >= usageDriftMemPct
}
