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
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/settings"
)

type fakeLeader bool

func (f fakeLeader) Leading() bool { return bool(f) }

// deferred collects scheduled checks so a test runs them when it chooses.
type deferred struct {
	delays []time.Duration
	fns    []func()
}

func (d *deferred) schedule(delay time.Duration, f func()) {
	d.delays = append(d.delays, delay)
	d.fns = append(d.fns, f)
}

func (d *deferred) run() {
	fns := d.fns
	d.fns, d.delays = nil, nil
	for _, f := range fns {
		f()
	}
}

func (h *harness) deferChecks() *deferred {
	d := &deferred{}
	h.svc.after = d.schedule
	return d
}

func TestNotifyChecksTheAppAfterTheGrace(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	d := h.deferChecks()
	*h.apps = fakeApps{containerApp(7, 3)}
	h.nodes.engines[3] = &fakeEngine{}
	h.releases[7] = "c7"

	h.svc.Notify(7)
	h.svc.Notify(7)
	if len(d.fns) != 1 || d.delays[0] != checkGrace {
		t.Fatalf("scheduled %d checks with delays %v; a burst of events schedules one after the grace", len(d.fns), d.delays)
	}
	if len(e.redeploy.calls) != 0 {
		t.Fatal("nothing may act before the grace")
	}
	d.run()
	if len(e.redeploy.calls) != 1 {
		t.Fatalf("calls = %+v; want one restore", e.redeploy.calls)
	}
	h.svc.Notify(7)
	if len(d.fns) != 1 {
		t.Fatal("a later event must schedule a new check")
	}
}

func TestNotifyIsDroppedOffTheLeaderOrWhenOff(t *testing.T) {
	h := newHarness()
	h.enforcing()
	d := h.deferChecks()
	h.svc.SetLeader(fakeLeader(false))
	h.svc.Notify(7)
	if len(d.fns) != 0 {
		t.Fatal("a standby control plane must not check: it holds no agent tunnels")
	}

	h.svc.SetLeader(fakeLeader(true))
	h.settings[settings.KeyControlManagerMode] = string(ModeOff)
	h.svc.Notify(7)
	if len(d.fns) != 0 {
		t.Fatal("mode off must not check")
	}
}

func TestLoopGuardPausesAnAppThatKeepsBeingRemoved(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	d := h.deferChecks()
	*h.apps = fakeApps{containerApp(7, 3)}
	h.nodes.engines[3] = &fakeEngine{}
	h.releases[7] = "c7"

	for i := range loopRestores + 1 {
		h.svc.Notify(7)
		d.run()
		// Past the backoff, so only the loop guard can hold the next restore back.
		h.now = h.now.Add(backoff(i+1) + time.Second)
	}
	if len(e.redeploy.calls) != loopRestores {
		t.Fatalf("calls = %d; want %d restores, then a pause", len(e.redeploy.calls), loopRestores)
	}
	if n := h.countEvents(models.EventReconcileBreakerOpen); n != 1 {
		t.Fatalf("breaker events = %d, want 1", n)
	}
	st := h.svc.Status()
	if len(st.Findings) != 1 || !st.Findings[0].BreakerOpen {
		t.Fatalf("findings = %+v; want the breaker shown open", st.Findings)
	}
}

func TestLoopGuardForgetsOldRestores(t *testing.T) {
	a := &attempt{}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	a.loopedLocked(now)
	a.loopedLocked(now.Add(time.Minute))
	if a.loopedLocked(now.Add(loopWindow + 2*time.Minute)) {
		t.Fatal("restores older than the window must not count")
	}
}
