// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package controlmanager

import (
	"errors"
	"fmt"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/settings"
)

type redeployCall struct {
	appID    uint
	serverID uint
	reason   string
}

type fakeRedeployer struct {
	calls  []redeployCall
	err    error
	nextID uint
}

func (f *fakeRedeployer) ReconcileRedeploy(app *models.Application, reason string) (*models.Deployment, error) {
	f.calls = append(f.calls, redeployCall{appID: app.ID, serverID: app.ServerID, reason: reason})
	if f.err != nil {
		return nil, f.err
	}
	f.nextID++
	return &models.Deployment{ID: f.nextID, ApplicationID: app.ID, Status: models.DeploymentPending}, nil
}

type fakePlacement struct{ cordoned map[uint]bool }

func (f fakePlacement) Placeable(serverID uint) error {
	if f.cordoned[serverID] {
		return errors.New("node is cordoned and cannot accept new placements")
	}
	return nil
}

type fakeConfigs map[uint]bool

func (f fakeConfigs) FindInWorkspace(_, id uint) (*models.Config, error) {
	if f[id] {
		return &models.Config{ID: id}, nil
	}
	return nil, errors.New("record not found")
}

type fakeAuditor struct{ entries []audit.Entry }

func (f *fakeAuditor) Record(e audit.Entry) { f.entries = append(f.entries, e) }

type enforcement struct {
	redeploy  *fakeRedeployer
	placement fakePlacement
	configs   fakeConfigs
	audit     *fakeAuditor
}

// enforcing switches the harness into enforce mode with everything acting requires.
func (h *harness) enforcing() *enforcement {
	e := &enforcement{
		redeploy:  &fakeRedeployer{},
		placement: fakePlacement{cordoned: map[uint]bool{}},
		configs:   fakeConfigs{},
		audit:     &fakeAuditor{},
	}
	h.settings[settings.KeyControlManagerMode] = string(ModeEnforce)
	h.svc.SetEnforcement(e.redeploy, e.placement, e.configs, e.audit)
	return e
}

func (h *harness) countEvents(t models.AppEventType) int {
	n := 0
	for _, ev := range h.events.events {
		if ev.typ == t {
			n++
		}
	}
	return n
}

func TestEnforceRedeploysAMissingContainerInPlace(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	*h.apps = fakeApps{containerApp(7, 3)}
	h.nodes.engines[3] = &fakeEngine{}
	h.releases[7] = "c7"

	h.sweep(t)
	if len(e.redeploy.calls) != 0 {
		t.Fatalf("calls = %+v; nothing may be redeployed before a finding is confirmed", e.redeploy.calls)
	}

	h.sweep(t)
	if len(e.redeploy.calls) != 1 {
		t.Fatalf("calls = %+v; want one redeploy once confirmed", e.redeploy.calls)
	}
	// In place: the app's own node, never a chosen one.
	if c := e.redeploy.calls[0]; c.appID != 7 || c.serverID != 3 || c.reason == "" {
		t.Fatalf("call = %+v; want app 7 redeployed on node 3 with a reason", c)
	}
	if h.countEvents(models.EventReconcileRedeploy) != 1 {
		t.Fatalf("events = %v; want one reconcile.redeploy", h.events.events)
	}
	// Unattended work signs itself: no actor, the control manager in metadata.
	if len(e.audit.entries) != 1 {
		t.Fatalf("audit = %+v; want one entry", e.audit.entries)
	}
	// The action names what it was performed on, so a node's gateway is never filed as an application.
	if a := e.audit.entries[0]; a.Action != "application.reconcile.restore" || a.TargetType != "application" ||
		a.ActorID != nil || a.Metadata["actor"] != actorName || a.TargetID != "7" {
		t.Fatalf("audit entry = %+v; want an unattended application.reconcile.restore for app 7", a)
	}
}

func TestObserveModeNeverRedeploys(t *testing.T) {
	h := newHarness()
	e := &fakeRedeployer{}
	h.svc.SetEnforcement(e, fakePlacement{cordoned: map[uint]bool{}}, fakeConfigs{}, &fakeAuditor{})
	*h.apps = fakeApps{containerApp(7, 1)}
	h.releases[7] = "c7"

	h.confirm(t)
	h.sweep(t)
	if len(e.calls) != 0 {
		t.Fatalf("calls = %+v; observe mode must act on nothing", e.calls)
	}
}

// The whole point of the guard: an app whose data is gone is never started again, however loudly its
// container is missing.
func TestEnforceRefusesWhenTheDataIsGone(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	appVolume(h, 7, 3, "mb-vol-1-data", "2026-09-01T10:00:00Z")
	h.nodes.engines[1].containers = nil // its container is gone too

	h.confirm(t)
	if len(e.redeploy.calls) != 0 {
		t.Fatalf("calls = %+v; an app with no data must not be redeployed", e.redeploy.calls)
	}
	if h.countEvents(models.EventReconcileBlocked) != 1 {
		t.Fatalf("events = %v; want one reconcile.blocked", h.events.events)
	}
	// The same refusal is not repeated every minute.
	h.sweep(t)
	h.sweep(t)
	if got := h.countEvents(models.EventReconcileBlocked); got != 1 {
		t.Fatalf("reconcile.blocked events = %d; want it said once", got)
	}
}

func TestEnforceRefusesOnACordonedNode(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	e.placement.cordoned[1] = true
	*h.apps = fakeApps{containerApp(7, 1)}
	h.releases[7] = "c7"

	h.confirm(t)
	if len(e.redeploy.calls) != 0 || h.countEvents(models.EventReconcileBlocked) != 1 {
		t.Fatalf("calls=%+v events=%v; a cordoned node is being drained, not filled", e.redeploy.calls, h.events.events)
	}
}

// "Only when the app has all its components": a config it mounts has to exist too.
func TestEnforceRefusesWhenAMountedConfigIsGone(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	app := containerApp(7, 1)
	app.Mounts = []models.AppMount{{ConfigID: 9, ConfigKey: "nginx.conf", Path: "/etc/nginx/nginx.conf"}}
	*h.apps = fakeApps{app}
	h.releases[7] = "c7"

	h.confirm(t)
	if len(e.redeploy.calls) != 0 || h.countEvents(models.EventReconcileBlocked) != 1 {
		t.Fatalf("calls=%+v events=%v; an app missing a config must not be redeployed", e.redeploy.calls, h.events.events)
	}

	e.configs[9] = true
	h.sweep(t)
	if len(e.redeploy.calls) != 1 {
		t.Fatalf("calls = %+v; want the redeploy once the config is back", e.redeploy.calls)
	}
}

func TestEnforceBacksOffBetweenAttempts(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	*h.apps = fakeApps{containerApp(7, 1)}
	h.releases[7] = "c7"

	h.sweep(t) // suspected
	h.sweep(t) // confirmed → first redeploy, next attempt due in one minute
	h.sweep(t) // due → second redeploy, next attempt due in two minutes
	if len(e.redeploy.calls) != 2 {
		t.Fatalf("calls = %d; want two attempts so far", len(e.redeploy.calls))
	}
	h.sweep(t) // one minute later: still inside the backoff
	if len(e.redeploy.calls) != 2 {
		t.Fatalf("calls = %d; the backoff was not honoured", len(e.redeploy.calls))
	}
	h.sweep(t) // two minutes after the last attempt
	if len(e.redeploy.calls) != 3 {
		t.Fatalf("calls = %d; want a third attempt once the backoff elapsed", len(e.redeploy.calls))
	}
}

// After enough failures it is not a blip: the control manager stops and says a person has to look.
func TestEnforceOpensTheBreakerAfterRepeatedFailures(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	e.redeploy.err = errors.New("the queue is down")
	*h.apps = fakeApps{containerApp(7, 1)}
	h.releases[7] = "c7"

	for range 64 {
		h.sweep(t)
		if h.countEvents(models.EventReconcileBreakerOpen) > 0 {
			break
		}
	}
	if h.countEvents(models.EventReconcileBreakerOpen) != 1 {
		t.Fatalf("events = %v; want one reconcile.breaker_open", h.events.events)
	}
	if len(e.redeploy.calls) != breakerAfter {
		t.Fatalf("calls = %d; want it to stop after %d failures", len(e.redeploy.calls), breakerAfter)
	}
	// The report says so, and nothing is attempted again.
	st := h.svc.Status()
	if len(st.Findings) != 1 || !st.Findings[0].BreakerOpen {
		t.Fatalf("findings = %+v; want the breaker reported open", st.Findings)
	}
	for range 64 {
		h.sweep(t)
	}
	if len(e.redeploy.calls) != breakerAfter {
		t.Fatalf("calls = %d; an open breaker must stop every further attempt", len(e.redeploy.calls))
	}
}

// A node that rebooted with many apps must not crowd out the deploys people are waiting on.
func TestEnforceRespectsThePerNodeBudget(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	apps := fakeApps{}
	for id := uint(1); id <= budgetPerNode+2; id++ {
		apps = append(apps, containerApp(id, 1))
		h.releases[id] = fmt.Sprintf("c%d", id)
	}
	*h.apps = apps

	h.confirm(t)
	if len(e.redeploy.calls) != budgetPerNode {
		t.Fatalf("calls = %d; want at most %d redeploys in flight on one node", len(e.redeploy.calls), budgetPerNode)
	}
}
