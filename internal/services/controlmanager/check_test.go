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
	"sync"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

func (h *harness) check(t *testing.T, appID uint) Status {
	t.Helper()
	if err := h.svc.Check(context.Background(), appID); err != nil {
		t.Fatal(err)
	}
	return h.svc.Status()
}

func TestCheckRestoresAContainerRemovedOutsideMiabiAtOnce(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	*h.apps = fakeApps{containerApp(7, 3)}
	h.nodes.engines[3] = &fakeEngine{}
	h.releases[7] = "c7"

	st := h.check(t, 7)
	if len(e.redeploy.calls) != 1 || e.redeploy.calls[0].appID != 7 {
		t.Fatalf("calls = %+v; the event and the check are two sightings, so one check acts", e.redeploy.calls)
	}
	if len(st.Findings) != 1 || st.Findings[0].Trigger != TriggerEvent {
		t.Fatalf("findings = %+v; want one confirmed by the event", st.Findings)
	}
	if got := e.audit.entries[len(e.audit.entries)-1].Metadata["trigger"]; got != TriggerEvent {
		t.Errorf("audit trigger = %v, want event", got)
	}

	h.sweep(t)
	if len(e.redeploy.calls) != 1 {
		t.Fatalf("calls = %+v; the sweep after the check must not redeploy again", e.redeploy.calls)
	}
}

func TestCheckLeavesAnIntactContainerAlone(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	*h.apps = fakeApps{containerApp(7, 3)}
	h.nodes.engines[3] = &fakeEngine{containers: []docker.Container{{ID: "c7"}}}
	h.releases[7] = "c7"

	if st := h.check(t, 7); len(st.Findings) != 0 || len(e.redeploy.calls) != 0 {
		t.Fatalf("findings = %+v, calls = %+v; a container that is back must not be touched", st.Findings, e.redeploy.calls)
	}
}

func TestCheckSkipsAnAppMiabiIsWorkingOn(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	*h.apps = fakeApps{containerApp(7, 3)}
	h.nodes.engines[3] = &fakeEngine{}
	h.releases[7] = "c7"
	h.sweep(t)
	if len(h.svc.Status().Findings) != 1 {
		t.Fatal("the sweep should have raised a finding")
	}

	h.deploys.inProgress = []uint{7}
	if st := h.check(t, 7); len(st.Findings) != 0 || len(e.redeploy.calls) != 0 {
		t.Fatalf("findings = %+v, calls = %+v; a redeploying app's finding settles and nothing acts", st.Findings, e.redeploy.calls)
	}
}

func TestCheckLeavesOtherAppsFindingsAlone(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	*h.apps = fakeApps{containerApp(7, 3), containerApp(8, 3)}
	h.nodes.engines[3] = &fakeEngine{containers: []docker.Container{{ID: "c7"}}}
	h.releases[7], h.releases[8] = "c7", "c8"
	h.sweep(t)

	st := h.check(t, 7)
	if len(st.Findings) != 1 || st.Findings[0].OwnerID != 8 || st.Findings[0].Confirmed {
		t.Fatalf("findings = %+v; app 8's unconfirmed finding must survive a check of app 7", st.Findings)
	}
	if st.LastSweepAt == nil {
		t.Fatal("a check must not reset the last sweep")
	}
	if len(e.redeploy.calls) != 0 {
		t.Fatalf("calls = %+v; app 8 is confirmed by sweeps, not by a check of app 7", e.redeploy.calls)
	}
}

func TestCheckFollowsTheAppsPolicy(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	app := containerApp(7, 3)
	app.ReconcilePolicy = models.ReconcileObserve
	*h.apps = fakeApps{app}
	h.nodes.engines[3] = &fakeEngine{}
	h.releases[7] = "c7"

	st := h.check(t, 7)
	if len(e.redeploy.calls) != 0 {
		t.Fatalf("calls = %+v; an observe app is reported, never redeployed", e.redeploy.calls)
	}
	if len(st.Findings) != 1 || !st.Findings[0].Confirmed {
		t.Fatalf("findings = %+v; want it reported", st.Findings)
	}
}

func TestSweepAndCheckRacingRedeployOnce(t *testing.T) {
	for range 20 {
		h := newHarness()
		e := h.enforcing()
		*h.apps = fakeApps{containerApp(7, 3)}
		h.nodes.engines[3] = &fakeEngine{}
		h.releases[7] = "c7"

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = h.svc.Sweep(context.Background()) }()
		go func() { defer wg.Done(); _ = h.svc.Check(context.Background(), 7) }()
		wg.Wait()
		if len(e.redeploy.calls) != 1 {
			t.Fatalf("calls = %+v; a sweep and a check of the same app must redeploy it once", e.redeploy.calls)
		}
	}
}
