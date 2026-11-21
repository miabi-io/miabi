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
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/models"
)

// startingEngine starts a container by flipping its state, or keeps it exited to model one that exits again.
type startingEngine struct {
	fakeEngine
	starts   []string
	staysOff bool
	startErr error
}

func (f *startingEngine) StartContainer(_ context.Context, id string) error {
	f.starts = append(f.starts, id)
	if f.startErr != nil {
		return f.startErr
	}
	if !f.staysOff {
		for i := range f.containers {
			if f.containers[i].ID == id {
				f.containers[i].State = "running"
			}
		}
	}
	return nil
}

func stoppedHarness(t *testing.T, optIn bool) (*harness, *enforcement, *startingEngine, *deferred) {
	t.Helper()
	h := newHarness()
	e := h.enforcing()
	d := h.deferChecks()
	app := containerApp(7, 3)
	app.RestartExternalStops = optIn
	*h.apps = fakeApps{app}
	h.releases[7] = "c7"
	eng := &startingEngine{fakeEngine: fakeEngine{containers: []docker.Container{{ID: "c7", State: "exited", ExitCode: 0}}}}
	h.nodes.engines[3] = &eng.fakeEngine
	h.svc.nodes = &startingNodes{fakeNodes: h.nodes, eng: eng, id: 3}
	return h, e, eng, d
}

// startingNodes hands out the starting engine for its node, so Start reaches it.
type startingNodes struct {
	*fakeNodes
	eng *startingEngine
	id  uint
}

func (n *startingNodes) For(serverID uint) (docker.Client, error) {
	if serverID == n.id {
		return n.eng, nil
	}
	return n.fakeNodes.For(serverID)
}

func TestStoppedContainerIsReportedButNotStartedWithoutOptIn(t *testing.T) {
	h, e, eng, _ := stoppedHarness(t, false)
	st := h.confirm(t)
	if len(st.Findings) != 1 || st.Findings[0].Class != drift.ClassStopped || st.Findings[0].Action != drift.ActionNone {
		t.Fatalf("findings = %+v; want one stopped finding Miabi only reports", st.Findings)
	}
	if len(eng.starts) != 0 || len(e.redeploy.calls) != 0 {
		t.Fatalf("starts = %v, redeploys = %v; nothing may act without restart_external_stops", eng.starts, e.redeploy.calls)
	}
}

func TestStoppedContainerIsStartedAgainWhenOptedIn(t *testing.T) {
	h, e, eng, d := stoppedHarness(t, true)
	h.check(t, 7)
	if len(eng.starts) != 1 || eng.starts[0] != "c7" {
		t.Fatalf("starts = %v; an event-confirmed stop is started on one check", eng.starts)
	}
	if len(e.redeploy.calls) != 0 {
		t.Fatalf("redeploys = %v; a stopped container is started, never redeployed", e.redeploy.calls)
	}
	if h.countEvents(models.EventReconcileStarted) != 1 {
		t.Fatal("want a reconcile.started event")
	}
	d.run()
	if st := h.sweep(t); len(st.Findings) != 0 {
		t.Fatalf("findings = %+v; the started container settles the finding", st.Findings)
	}
}

func TestStartThatExitsAgainCountsAsFailed(t *testing.T) {
	h, _, eng, d := stoppedHarness(t, true)
	eng.staysOff = true
	h.check(t, 7)
	if len(d.fns) != 1 {
		t.Fatalf("scheduled %d re-exit checks, want 1", len(d.fns))
	}
	d.run()
	h.svc.mu.Lock()
	failures := h.svc.attempts[appKey(7)].failures
	h.svc.mu.Unlock()
	if failures != 1 {
		t.Fatalf("failures = %d; a container that exits again within the window is a failed attempt", failures)
	}
}

func TestStartErrorIsAFailedAttempt(t *testing.T) {
	h, _, eng, _ := stoppedHarness(t, true)
	eng.startErr = errors.New("boom")
	h.check(t, 7)
	h.svc.mu.Lock()
	failures := h.svc.attempts[appKey(7)].failures
	h.svc.mu.Unlock()
	if failures != 1 {
		t.Fatalf("failures = %d, want 1", failures)
	}
}
