// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package controlmanager

import (
	"fmt"
	"sort"
	"time"

	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/metrics"
	"github.com/miabi-io/miabi/internal/models"
)

// Finding is an app a sweep found missing. Findings live only in the leading process's memory, so a new
// leader starts over and confirms again before it reports anything.
type Finding struct {
	drift.Item
	WorkspaceID uint      `json:"workspace_id"`
	ServerID    uint      `json:"server_id"`
	ClusterID   uint      `json:"cluster_id"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	// Confirmed is set once enough sweeps agree. Only confirmed findings are recorded as events and counted
	// in metrics.
	Confirmed bool `json:"confirmed"`

	misses int
}

// Status is the control manager's last sweep.
type Status struct {
	Mode        Mode       `json:"mode"`
	LastSweepAt *time.Time `json:"last_sweep_at"`
	SweepMillis int64      `json:"sweep_ms"`
	Findings    []Finding  `json:"findings"`
	Skipped     []Skip     `json:"skipped"`
}

// Status reports the last sweep. A standby control plane never sweeps, so it has none to report.
func (s *Service) Status() Status {
	out := Status{Mode: s.Mode(), Findings: []Finding{}, Skipped: []Skip{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastSweep != nil {
		at := *s.lastSweep
		out.LastSweepAt = &at
		out.SweepMillis = s.sweepTook.Milliseconds()
	}
	for _, f := range s.findings {
		out.Findings = append(out.Findings, *f)
	}
	sort.Slice(out.Findings, func(i, j int) bool { return out.Findings[i].OwnerID < out.Findings[j].OwnerID })
	out.Skipped = append(out.Skipped, s.skipped...)
	return out
}

// notice is a timeline event decided under the lock and emitted after it.
type notice struct {
	app     models.Application
	finding Finding
	back    bool
}

// record folds one sweep into the findings. An unknown observation neither advances nor clears a finding:
// only a sweep that can see the app changes what is known about it.
func (s *Service) record(apps []models.Application, seen map[uint]observation, skipped []Skip, start time.Time) {
	now := s.now()
	var notices []notice

	s.mu.Lock()
	expected := make(map[uint]bool, len(apps))
	for i := range apps {
		a := apps[i]
		expected[a.ID] = true
		o := seen[a.ID]
		f := s.findings[a.ID]
		switch o.presence {
		case present:
			if f != nil {
				delete(s.findings, a.ID)
				if f.Confirmed {
					notices = append(notices, notice{app: a, finding: *f, back: true})
				}
			}
		case missing:
			if f == nil {
				f = newFinding(&a, o.kind, now)
				s.findings[a.ID] = f
			}
			f.misses++
			f.LastSeenAt = now
			if !f.Confirmed && f.misses >= confirmAfter {
				f.Confirmed = true
				notices = append(notices, notice{app: a, finding: *f})
			}
		}
	}
	// An app no longer expected was deleted, stopped or is being redeployed, which settles its finding.
	for id := range s.findings {
		if !expected[id] {
			delete(s.findings, id)
		}
	}
	s.skipped = skipped
	s.lastSweep = &start
	took := now.Sub(start)
	s.sweepTook = took
	containers, services := s.confirmedLocked()
	s.mu.Unlock()

	nodes, clusters := 0, 0
	for _, sk := range skipped {
		if sk.Scope == "cluster" {
			clusters++
		} else {
			nodes++
		}
	}
	metrics.ObserveControlManagerSweep(took)
	metrics.SetControlManagerDrift(containers, services, nodes, clusters)
	for _, n := range notices {
		s.announce(n)
	}
}

func (s *Service) reset() {
	s.mu.Lock()
	s.findings = map[uint]*Finding{}
	s.skipped = nil
	s.lastSweep = nil
	s.sweepTook = 0
	s.mu.Unlock()
	metrics.SetControlManagerDrift(0, 0, 0, 0)
}

func (s *Service) confirmedLocked() (containers, services int) {
	for _, f := range s.findings {
		switch {
		case !f.Confirmed:
		case f.Kind == kindService:
			services++
		default:
			containers++
		}
	}
	return containers, services
}

func newFinding(a *models.Application, kind string, now time.Time) *Finding {
	return &Finding{
		Item: drift.Item{
			Class: drift.ClassMissing, Kind: kind, Ref: fmt.Sprintf("app:%d", a.ID), Name: a.Name,
			OwnerKind: drift.OwnerApp, OwnerID: a.ID, Action: drift.ActionRedeploy,
		},
		WorkspaceID: a.WorkspaceID, ServerID: a.ServerID, ClusterID: a.ClusterID,
		FirstSeenAt: now, LastSeenAt: now,
	}
}

var detectedMessages = map[string]string{
	kindContainer: "The active release's container no longer exists on its node",
	kindService:   "The app's swarm service no longer exists in its cluster",
}

var resolvedMessages = map[string]string{
	kindContainer: "The active release's container exists again",
	kindService:   "The app's swarm service exists again",
}

func (s *Service) announce(n notice) {
	meta := map[string]string{
		"actor":         "control-manager",
		"kind":          n.finding.Kind,
		"first_seen_at": n.finding.FirstSeenAt.UTC().Format(time.RFC3339),
	}
	if n.back {
		s.events.Emit(n.app.WorkspaceID, n.app.ID, models.EventDriftResolved, models.SeverityInfo, resolvedMessages[n.finding.Kind], meta, nil)
		return
	}
	s.events.Emit(n.app.WorkspaceID, n.app.ID, models.EventDriftDetected, models.SeverityWarning, detectedMessages[n.finding.Kind], meta, nil)
}
