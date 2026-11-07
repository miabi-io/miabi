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

package controlmanager

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/miabi-io/miabi/internal/metrics"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
)

const (
	// An app that cannot be brought back is retried ever more slowly rather than every minute.
	backoffBase = time.Minute
	backoffMax  = 30 * time.Minute
	// Past this point it is not a blip, and a person has to look.
	breakerAfter = 5
	// A flapping app keeps its backoff instead of resetting after one healthy sweep.
	forgetAfter = 10 * time.Minute
	// A rebooted node with forty apps must not crowd out the deploys people are waiting on.
	budgetPerNode  = 3
	budgetPlatform = 10
)

// The deploy is routed by the app's own ServerID and ClusterID, so nothing is placed or moved.
type Redeployer interface {
	ReconcileRedeploy(app *models.Application, reason string) (*models.Deployment, error)
}

// A cordoned node is being drained; recreating containers there works against the operator.
type NodePlacement interface {
	Placeable(serverID uint) error
}

// ConfigStore resolves a config an app mounts. Satisfied by *repositories.ConfigRepository.
type ConfigStore interface {
	FindInWorkspace(workspaceID, id uint) (*models.Config, error)
}

// So an app that restarted itself at 03:00 is explainable without reading logs.
type Auditor interface {
	Record(e audit.Entry)
}

// Without a Redeployer nothing is redeployed, whatever the mode says.
func (s *Service) SetEnforcement(r Redeployer, placement NodePlacement, configs ConfigStore, auditor Auditor) {
	s.redeployer, s.placement, s.configs, s.auditor = r, placement, configs, auditor
}

// SetSuspended wires the suspended-workspace check, so enforcement never restarts what a suspension stopped.
func (s *Service) SetSuspended(f func(workspaceID uint) bool) { s.suspended = f }

// Outlives the finding by forgetAfter so a flapping app cannot reset its own backoff.
type attempt struct {
	tries       int
	failures    int
	breakerOpen bool
	lastActedAt time.Time
	nextAt      time.Time
	// So a reason the operator must fix isn't repeated every minute.
	saidBlocked string
}

func backoff(tries int) time.Duration {
	d := backoffBase << max(tries-1, 0)
	if d > backoffMax || d <= 0 {
		return backoffMax
	}
	return d
}

// Sees only confirmed findings, and runs only on the leading control plane.
func (s *Service) act(ctx context.Context, items []item) {
	// Per item canAct has the final say, since an app may enforce while the platform observes.
	if s.Mode() == ModeOff {
		return
	}
	perNode, total := s.inflight()
	now := s.now()

	// Oldest first, so something long broken is not starved by a newer finding while the budget is tight.
	for _, d := range s.due(items, now) {
		if reason := s.refuse(d.it); reason != "" {
			s.reportBlocked(d, reason)
			continue
		}
		if total >= budgetPlatform || perNode[d.it.nodeID] >= budgetPerNode {
			metrics.ControlManagerAction(d.action(), "deferred")
			continue
		}
		dep, err := s.restore(ctx, d)
		if err != nil {
			s.failed(d, err)
			continue
		}
		total++
		perNode[d.it.nodeID]++
		s.started(d, dep)
	}
}

func (s *Service) restore(ctx context.Context, d actionable) (*models.Deployment, error) {
	if d.it.kind == kindGateway {
		return nil, s.gateways.EnsureGateway(ctx, d.it.nodeID)
	}
	return s.redeployer.ReconcileRedeploy(d.it.app, d.reason())
}

// actionable is a confirmed finding that enforcement may act on now.
type actionable struct {
	it *item
	f  Finding
}

// reason is what the action is recorded as having been for.
func (a actionable) reason() string {
	switch a.f.Kind {
	case kindGateway:
		return "the node's gateway was not running"
	case kindService:
		return "the app's swarm service no longer existed in its cluster"
	default:
		return "the app's container no longer existed on its node"
	}
}

// action names what enforcement does about this kind, for the metric and the audit trail.
func (a actionable) action() string {
	if a.f.Kind == kindGateway {
		return "gateway_ensure"
	}
	return "redeploy"
}

// Never volumes (lost data needs a person) or imported gateways (someone else's container).
func (s *Service) canAct(it *item) bool {
	switch it.kind {
	case kindContainer, kindService:
		// The app's own policy decides, which is the platform's unless it overrides it.
		return it.app != nil && s.redeployer != nil && it.policy == ModeEnforce
	case kindGateway:
		// A gateway has no app to carry a policy, so it follows the platform.
		return !it.imported && s.gateways != nil && s.Mode() == ModeEnforce
	default:
		return false
	}
}

func (s *Service) due(items []item, now time.Time) []actionable {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []actionable
	for i := range items {
		it := &items[i]
		if !s.canAct(it) {
			continue
		}
		f := s.findings[it.key]
		if f == nil || !f.Confirmed {
			continue
		}
		if a := s.attempts[it.key]; a != nil && (a.breakerOpen || now.Before(a.nextAt)) {
			continue
		}
		out = append(out, actionable{it: it, f: *f})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].f.FirstSeenAt.Before(out[j].f.FirstSeenAt) })
	return out
}

// An app is only brought back with its data, a node accepting workloads, and its mounted configs.
func (s *Service) refuse(it *item) string {
	// A gateway is the node's ingress rather than a workload placed on it, so even a draining node wants one.
	if it.kind == kindGateway {
		return ""
	}
	if reason, blocked := s.BlockedRedeploy(it.id); blocked {
		return reason
	}
	if s.suspended != nil && it.app != nil && s.suspended(it.app.WorkspaceID) {
		return "its workspace is suspended by the platform administrator"
	}
	if s.placement != nil {
		if err := s.placement.Placeable(it.nodeID); err != nil {
			return "its node cannot take a workload: " + err.Error()
		}
	}
	if s.configs != nil && it.app != nil {
		for _, m := range it.app.Mounts {
			if m.ConfigID == 0 {
				continue
			}
			if _, err := s.configs.FindInWorkspace(it.workspaceID, m.ConfigID); err != nil {
				return fmt.Sprintf("a config it mounts (#%d) no longer exists", m.ConfigID)
			}
		}
	}
	return ""
}

func (s *Service) inflight() (map[uint]int, int) {
	perNode := map[uint]int{}
	total := 0
	s.mu.Lock()
	pending := make(map[uint]uint, len(s.inflightDeploys))
	for depID, nodeID := range s.inflightDeploys {
		pending[depID] = nodeID
	}
	s.mu.Unlock()

	done := make([]uint, 0, len(pending))
	for depID, nodeID := range pending {
		dep, err := s.deploys.FindByID(depID)
		if err != nil || dep.Status.IsTerminal() {
			done = append(done, depID)
			continue
		}
		perNode[nodeID]++
		total++
	}
	if len(done) > 0 {
		s.mu.Lock()
		for _, depID := range done {
			delete(s.inflightDeploys, depID)
		}
		s.mu.Unlock()
	}
	return perNode, total
}

func (s *Service) started(d actionable, dep *models.Deployment) {
	now := s.now()
	s.mu.Lock()
	a := s.attemptLocked(d.it.key)
	a.tries++
	a.failures = 0
	a.lastActedAt = now
	a.nextAt = now.Add(backoff(a.tries))
	a.saidBlocked = ""
	if dep != nil {
		s.inflightDeploys[dep.ID] = d.it.nodeID
	}
	tries := a.tries
	s.mu.Unlock()

	metrics.ControlManagerAction(d.action(), "started")
	meta := map[string]string{"actor": actorName, "kind": d.f.Kind, "attempt": strconv.Itoa(tries)}
	if dep != nil {
		meta["deployment_id"] = strconv.FormatUint(uint64(dep.ID), 10)
	}
	s.emitFor(d, models.EventReconcileRedeploy, models.SeverityWarning,
		"Restored by the control manager: "+d.reason(), meta)
	s.record4audit(d, "reconcile.restore", map[string]any{"reason": d.reason(), "attempt": tries})
}

func (s *Service) failed(d actionable, err error) {
	now := s.now()
	s.mu.Lock()
	a := s.attemptLocked(d.it.key)
	a.tries++
	a.failures++
	a.lastActedAt = now
	a.nextAt = now.Add(backoff(a.tries))
	opened := a.failures >= breakerAfter && !a.breakerOpen
	if opened {
		a.breakerOpen = true
	}
	failures := a.failures
	s.mu.Unlock()

	metrics.ControlManagerAction(d.action(), "failed")
	if !opened {
		return
	}
	metrics.ControlManagerAction("breaker", "open")
	meta := map[string]string{"actor": actorName, "kind": d.f.Kind, "failures": strconv.Itoa(failures), "error": err.Error()}
	s.emitFor(d, models.EventReconcileBreakerOpen, models.SeverityError,
		fmt.Sprintf("The control manager gave up after %d failed attempts: %v", failures, err), meta)
	s.record4audit(d, "reconcile.breaker_open", map[string]any{"failures": failures, "error": err.Error()})
}

// Repeating it every minute would bury the timeline in something only a person can resolve.
func (s *Service) reportBlocked(d actionable, reason string) {
	s.mu.Lock()
	a := s.attemptLocked(d.it.key)
	repeat := a.saidBlocked == reason
	a.saidBlocked = reason
	s.mu.Unlock()
	if repeat {
		return
	}
	metrics.ControlManagerAction(d.action(), "blocked")
	s.emitFor(d, models.EventReconcileBlocked, models.SeverityWarning,
		"Not restored by the control manager: "+reason, map[string]string{"actor": actorName, "kind": d.f.Kind})
}

// A gateway has no timeline of its own, so its actions live in the report, metrics and audit log.
func (s *Service) emitFor(d actionable, t models.AppEventType, sev models.AppEventSeverity, msg string, meta map[string]string) {
	if d.it.kind == kindGateway || d.it.workspaceID == 0 {
		return
	}
	s.events.Emit(d.it.workspaceID, d.it.id, t, sev, msg, meta, nil)
}

// attemptLocked returns the item's attempt record, creating it on first use. Caller holds the lock.
func (s *Service) attemptLocked(key string) *attempt {
	a := s.attempts[key]
	if a == nil {
		a = &attempt{}
		s.attempts[key] = a
	}
	return a
}

// Nil ActorID plus the actor name in metadata is how a system job signs its work.
func (s *Service) record4audit(d actionable, action string, meta map[string]any) {
	if s.auditor == nil {
		return
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["actor"] = actorName
	target := "application"
	if d.f.Kind == kindGateway {
		target = "node"
	}
	entry := audit.Entry{
		Action:     target + "." + action,
		TargetType: target,
		TargetID:   strconv.FormatUint(uint64(d.it.id), 10),
		Metadata:   meta,
	}
	// A gateway's finding is node-scoped: there is no workspace to attribute it to.
	if ws := d.it.workspaceID; ws != 0 {
		entry.WorkspaceID = &ws
	}
	s.auditor.Record(entry)
}
