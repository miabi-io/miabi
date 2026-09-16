// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package controlmanager

import (
	"sort"
	"time"

	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/metrics"
	"github.com/miabi-io/miabi/internal/models"
)

// Restore is the backup a report of lost data points at. Miabi never restores one by itself: which
// recovery point to accept, and the data loss between it and now, are a person's call.
type Restore struct {
	// From names where the data comes back from: a volume archive, or a database recovery point.
	From string `json:"from"`
	// Available is false when there is no completed backup to restore, which is the worst case and the
	// one worth saying out loud.
	Available bool      `json:"available"`
	BackupID  uint      `json:"backup_id,omitempty"`
	Ref       string    `json:"ref,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	SizeBytes int64     `json:"size_bytes,omitempty"`
}

// Finding is a workload a sweep found missing or replaced. Findings live only in the leading process's
// memory, so a new leader starts over and confirms again before it reports anything.
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
	// Attempts, NextAttemptAt and BreakerOpen report what enforcement has tried: how many redeploys, when the
	// next one is due under backoff, and whether it has given up and left this to a person.
	Attempts      int        `json:"attempts,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	BreakerOpen   bool       `json:"breaker_open,omitempty"`
	// Restore is set on lost data: the backup to bring it back from.
	Restore *Restore `json:"restore,omitempty"`
	// Blocked marks a workload nothing may start again, because the volume holding its data is gone.
	Blocked bool `json:"blocked,omitempty"`
	// BlockedReason says which volume, for a report that stands on its own.
	BlockedReason string `json:"blocked_reason,omitempty"`

	misses   int
	subjects []subject
}

// BlockedApp is an app whose data is gone. Starting it again would give it an empty volume and call that
// recovery, so it needs a person to restore the volume first — Miabi cannot put the data back.
type BlockedApp struct {
	AppID  uint   `json:"app_id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Status is the control manager's last sweep.
type Status struct {
	Mode        Mode         `json:"mode"`
	LastSweepAt *time.Time   `json:"last_sweep_at"`
	SweepMillis int64        `json:"sweep_ms"`
	Findings    []Finding    `json:"findings"`
	Blocked     []BlockedApp `json:"blocked"`
	Skipped     []Skip       `json:"skipped"`
}

// Status reports the last sweep. A standby control plane never sweeps, so it has none to report.
func (s *Service) Status() Status {
	out := Status{Mode: s.Mode(), Findings: []Finding{}, Blocked: []BlockedApp{}, Skipped: []Skip{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastSweep != nil {
		at := *s.lastSweep
		out.LastSweepAt = &at
		out.SweepMillis = s.sweepTook.Milliseconds()
	}
	for key, f := range s.findings {
		cp := *f
		if a := s.attempts[key]; a != nil {
			cp.Attempts, cp.BreakerOpen = a.tries, a.breakerOpen
			if !a.nextAt.IsZero() {
				next := a.nextAt
				cp.NextAttemptAt = &next
			}
		}
		out.Findings = append(out.Findings, cp)
	}
	sort.Slice(out.Findings, func(i, j int) bool {
		if out.Findings[i].Kind != out.Findings[j].Kind {
			return out.Findings[i].Kind < out.Findings[j].Kind
		}
		return out.Findings[i].OwnerID < out.Findings[j].OwnerID
	})
	out.Blocked = append(out.Blocked, s.blockedList()...)
	out.Skipped = append(out.Skipped, s.skipped...)
	return out
}

// BlockedRedeploy reports whether an app must not be started again, and why. Whatever comes to act on
// findings must ask first: Docker creates a named volume silently when a container starts without one, so
// redeploying an app whose volume is gone turns data loss into an empty volume nobody notices.
func (s *Service) BlockedRedeploy(appID uint) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason, ok := s.blocked[appID]
	return reason, ok
}

func (s *Service) blockedList() []BlockedApp {
	out := make([]BlockedApp, 0, len(s.blocked))
	for id, reason := range s.blocked {
		b := BlockedApp{AppID: id, Reason: reason}
		if f, ok := s.findings[appKey(id)]; ok {
			b.Name = f.Name
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })
	return out
}

// notice is a timeline event decided under the lock and emitted after it.
type notice struct {
	finding Finding
	back    bool
}

// record folds one sweep into the findings. An unknown observation neither advances nor clears a finding:
// only a sweep that could see the item changes what is known about it.
func (s *Service) record(items []item, seen map[string]observation, skipped []Skip, start time.Time) {
	now := s.now()
	var notices []notice

	s.mu.Lock()
	expected := make(map[string]bool, len(items))
	for i := range items {
		it := &items[i]
		expected[it.key] = true
		o := seen[it.key]
		f := s.findings[it.key]
		switch o.state {
		case intact:
			if f != nil {
				delete(s.findings, it.key)
				if f.Confirmed {
					notices = append(notices, notice{finding: *f, back: true})
				}
			}
		case gone, replaced:
			if f == nil || f.Class != o.state.class() {
				f = newFinding(it, o.state, now)
				s.findings[it.key] = f
			}
			f.misses++
			f.LastSeenAt = now
			if !f.Confirmed && f.misses >= confirmAfter {
				f.Confirmed = true
				notices = append(notices, notice{finding: *f})
			}
		}
	}
	// An item no longer expected was deleted, stopped or is being redeployed, which settles its finding.
	for key := range s.findings {
		if !expected[key] {
			delete(s.findings, key)
		}
	}
	// An item's attempt history outlives its finding for a while, so an app that keeps disappearing does not
	// reset its own backoff by looking healthy for one sweep.
	for key, a := range s.attempts {
		if _, still := s.findings[key]; !still && now.Sub(a.lastActedAt) > forgetAfter {
			delete(s.attempts, key)
		}
	}
	s.blocked = s.blockLocked(items)
	s.skipped = skipped
	s.lastSweep = &start
	took := now.Sub(start)
	s.sweepTook = took
	counts := s.countsLocked()
	s.mu.Unlock()

	for _, sk := range skipped {
		if sk.Scope == "cluster" {
			counts.UnobservedClusters++
		} else {
			counts.UnobservedNodes++
		}
	}
	metrics.ObserveControlManagerSweep(took)
	metrics.SetControlManagerDrift(counts)
	for _, n := range notices {
		s.announce(n)
	}
}

// blockLocked lists the apps whose data is gone, and marks their own findings so a report of a missing
// container says a redeploy is not the answer. A lost volume is only confirmed after two sweeps, so an
// app is never blocked on one bad look at a node.
func (s *Service) blockLocked(items []item) map[uint]string {
	out := map[uint]string{}
	for i := range items {
		it := &items[i]
		if it.owner != drift.OwnerApp {
			continue
		}
		for _, need := range it.needs {
			lost, ok := s.findings[need]
			if !ok || !lost.Confirmed {
				continue
			}
			reason := "its data volume " + lost.Name + " is " + lost.Class + ", so it needs a manual restore before it runs again"
			out[it.id] = reason
			if f, ok := s.findings[it.key]; ok {
				f.Blocked, f.BlockedReason = true, reason
				// Redeploying would start the app on an empty volume; the volume has to come back first.
				f.Action = drift.ActionRestore
			}
		}
	}
	return out
}

func (s *Service) reset() {
	s.mu.Lock()
	s.findings = map[string]*Finding{}
	s.blocked = map[uint]string{}
	s.attempts = map[string]*attempt{}
	s.inflightDeploys = map[uint]uint{}
	s.skipped = nil
	s.lastSweep = nil
	s.sweepTook = 0
	s.mu.Unlock()
	metrics.SetControlManagerDrift(metrics.ControlManagerDrift{})
}

func (s *Service) countsLocked() metrics.ControlManagerDrift {
	var c metrics.ControlManagerDrift
	for _, f := range s.findings {
		switch {
		case !f.Confirmed:
		case f.Kind == kindService:
			c.MissingServices++
		case f.Kind == kindVolume && f.Class == drift.ClassReplaced:
			c.ReplacedVolumes++
		case f.Kind == kindVolume:
			c.MissingVolumes++
		default:
			c.MissingContainers++
		}
	}
	c.BlockedApps = len(s.blocked)
	return c
}

func newFinding(it *item, st state, now time.Time) *Finding {
	f := &Finding{
		Item: drift.Item{
			Class: st.class(), Kind: it.kind, Ref: it.ref, Name: it.name,
			OwnerKind: it.owner, OwnerID: it.id, Action: drift.ActionRedeploy,
		},
		WorkspaceID: it.workspaceID, ServerID: it.nodeID, ClusterID: it.clusterID,
		FirstSeenAt: now, LastSeenAt: now,
		subjects: it.subjects,
	}
	if it.kind == kindVolume {
		// Recreating a volume gives an empty one, which is data loss dressed up as self-healing. The way
		// back is a backup, and choosing one is a person's call.
		f.Action = drift.ActionRestore
		if it.restore != nil {
			f.Restore = it.restore()
			if f.Restore != nil {
				f.Restore.Available = f.Restore.BackupID != 0
			}
		}
	}
	return f
}

var detectedMessages = map[string]string{
	kindContainer: "The active release's container no longer exists on its node",
	kindService:   "The app's swarm service no longer exists in its cluster",
}

var resolvedMessages = map[string]string{
	kindContainer: "The active release's container exists again",
	kindService:   "The app's swarm service exists again",
}

// message describes a finding for the timeline. A volume names itself, since the event is recorded on the
// timeline of whatever mounts it rather than the volume's own, and it says plainly that Miabi will not put
// the data back.
func message(f Finding, back bool) string {
	if f.Kind != kindVolume {
		if back {
			return resolvedMessages[f.Kind]
		}
		return detectedMessages[f.Kind]
	}
	if back {
		return "The data volume " + f.Name + " exists again"
	}
	lost := "The data volume " + f.Name + " no longer exists on its node"
	if f.Class == drift.ClassReplaced {
		lost = "The data volume " + f.Name + " was deleted and recreated outside Miabi, so it no longer holds the data it did"
	}
	return lost + ". " + restoreAdvice(f.Restore) + " Miabi will not recreate the volume, and nothing that mounts it is started again until it is back."
}

// restoreAdvice suggests the backup to restore, or says there is none — which an operator needs to hear
// immediately, not discover while looking for one.
func restoreAdvice(r *Restore) string {
	switch {
	case r == nil:
		return "Restore it from a backup."
	case !r.Available && r.From == "recovery-point":
		return "No completed recovery point was found for this instance."
	case !r.Available:
		return "No completed backup was found for this volume."
	case r.From == "recovery-point":
		return "Restore recovery point " + r.Ref + " from " + r.CreatedAt.UTC().Format(time.RFC3339) + "."
	default:
		return "Restore the backup from " + r.CreatedAt.UTC().Format(time.RFC3339) + "."
	}
}

// announce records a confirmed finding, and its recovery, on the timeline of everything it affects: the
// app itself, every app mounting a lost volume, or the database instance whose data it was.
func (s *Service) announce(n notice) {
	f := n.finding
	meta := map[string]string{
		"actor":         "control-manager",
		"kind":          f.Kind,
		"class":         f.Class,
		"first_seen_at": f.FirstSeenAt.UTC().Format(time.RFC3339),
	}
	if f.Kind == kindVolume {
		meta["volume"] = f.Name
		if f.Restore != nil && f.Restore.Available {
			meta["restore_from"] = f.Restore.Ref
		}
	}
	typ, sev := models.EventDriftDetected, models.SeverityWarning
	if f.Kind == kindVolume {
		// Data that is gone is not a warning: nothing brings it back but a restore.
		sev = models.SeverityError
	}
	if n.back {
		typ, sev = models.EventDriftResolved, models.SeverityInfo
	}
	msg := message(f, n.back)
	for _, sub := range f.subjects {
		if sub.databaseID != 0 {
			s.events.EmitDatabase(sub.workspaceID, sub.databaseID, sub.databaseName, typ, sev, msg, meta, nil)
			continue
		}
		s.events.Emit(sub.workspaceID, sub.appID, typ, sev, msg, meta, nil)
	}
}
