// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// backoffBase doubles per attempt up to backoffMax, so an app that cannot be brought back is retried
	// ever more slowly rather than every minute forever.
	backoffBase = time.Minute
	backoffMax  = 30 * time.Minute
	// breakerAfter consecutive failed redeploys stops the control manager touching an item at all: past this
	// point it is not a blip, and a person has to look at it.
	breakerAfter = 5
	// forgetAfter is how long an item's attempt history outlives its finding, so an app that flaps keeps its
	// backoff instead of resetting it every time it looks healthy for a single sweep.
	forgetAfter = 10 * time.Minute
	// The budgets cap how many redeploys the control manager may have in flight, so a node that rebooted with
	// forty apps cannot crowd out the deploys people are waiting on.
	budgetPerNode  = 3
	budgetPlatform = 10
)

// Redeployer redeploys an app where it already is: the deploy is routed by the app's own ServerID and
// ClusterID, so nothing is ever placed or moved. Implemented by *application.Service.
type Redeployer interface {
	ReconcileRedeploy(app *models.Application, reason string) (*models.Deployment, error)
}

// NodePlacement reports whether a node may take a workload. Satisfied by *node.Service: a cordoned node is
// being drained, and recreating containers on it would work against the operator who cordoned it.
type NodePlacement interface {
	Placeable(serverID uint) error
}

// ConfigStore resolves a config an app mounts. Satisfied by *repositories.ConfigRepository.
type ConfigStore interface {
	FindInWorkspace(workspaceID, id uint) (*models.Config, error)
}

// Auditor records what the control manager did, so an app that restarted itself at 03:00 is explainable
// without reading logs. Satisfied by *audit.Logger.
type Auditor interface {
	Record(e audit.Entry)
}

// SetEnforcement wires what acting on a finding needs. Without a Redeployer nothing is ever redeployed,
// whatever the mode says.
func (s *Service) SetEnforcement(r Redeployer, placement NodePlacement, configs ConfigStore, auditor Auditor) {
	s.redeployer, s.placement, s.configs, s.auditor = r, placement, configs, auditor
}

// attempt is what enforcement has tried for one item. It outlives the finding by forgetAfter, so a flapping
// app cannot reset its own backoff.
type attempt struct {
	tries       int
	failures    int
	breakerOpen bool
	lastActedAt time.Time
	nextAt      time.Time
	// saidBlocked is the last refusal reported, so a reason the operator has to fix is not repeated every
	// minute until they do.
	saidBlocked string
}

func backoff(tries int) time.Duration {
	d := backoffBase << max(tries-1, 0)
	if d > backoffMax || d <= 0 {
		return backoffMax
	}
	return d
}

// act restores what it can, in place. It runs after record, so it sees only confirmed findings, and only on
// the leading control plane — the cron manager runs the sweep nowhere else.
func (s *Service) act(ctx context.Context, items []item) {
	if s.Mode() != ModeEnforce {
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

// restore performs the one action the item's kind allows: a node's gateway is re-ensured on it, an app is
// redeployed through the ordinary deploy path.
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

// canAct reports whether enforcement may ever act on an item. Volumes never: lost data is restored by a
// person. An imported gateway never either — its container belongs to whoever installed it, which on the
// manager is the platform stack itself.
func (s *Service) canAct(it *item) bool {
	switch it.kind {
	case kindContainer, kindService:
		return it.app != nil && s.redeployer != nil
	case kindGateway:
		return !it.imported && s.gateways != nil
	default:
		return false
	}
}

// due picks the confirmed findings enforcement may act on whose backoff has elapsed and whose breaker is
// closed.
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

// refuse names why an app must not be redeployed, or "" when it is whole. An app is only brought back with
// everything it needs: its data, a node that accepts workloads, and the configs it mounts.
func (s *Service) refuse(it *item) string {
	// A gateway is the node's ingress rather than a workload placed on it, so even a draining node wants one.
	if it.kind == kindGateway {
		return ""
	}
	if reason, blocked := s.BlockedRedeploy(it.id); blocked {
		return reason
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

// inflight counts the redeploys the control manager has started that have not finished, per node and in
// total, dropping the ones that reached a terminal state.
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

// reportBlocked says once why an app the control manager would have redeployed was left alone. Repeating it
// every minute would bury the timeline in something only a person can resolve.
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

// emitFor records an action on the timeline of the app it concerns. A gateway belongs to a node rather than a
// workspace and has no timeline of its own, so its actions live in the report, the metrics and the audit log.
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

// record4audit writes the audit trail for an unattended action. There is no actor: nil ActorID plus the
// actor name in metadata is how a system job signs its work.
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
