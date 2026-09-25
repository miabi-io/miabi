// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// persistEvery bounds servers writes from pushes to about one per node per minute, so a fleet
	// pushing every 15s does not quadruple the write rate.
	persistEvery = time.Minute
	// Movement that is written straight away rather than waiting out persistEvery.
	usageDriftCPUPoints = 10.0
	usageDriftMemPct    = 5
)

// ErrInvalidReport is returned for a pushed report whose figures are nonsense. It is rejected rather
// than clamped so a broken agent shows as unmeasured instead of as an idle node.
var ErrInvalidReport = errors.New("invalid host stats report")

// SetAgentStats toggles use of agent-pushed readings (MIABI_NODE_STATS_AGENT). Off, pushes are
// ignored and every remote node is sampled by container as before.
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

// Ingest records a report pushed by srv's agent, stamped with the control plane's receipt time. The
// node's usage columns are written at most once per persistEvery, or sooner on real movement.
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
