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

package worker

import (
	"context"
	"reflect"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// switchFake also records service removals, which a runtime switch from service has to go through.
type switchFake struct{ stopFake }

func (f switchFake) ServiceRemove(_ context.Context, id string) error {
	f.log.steps = append(f.log.steps, "service-rm "+id)
	return nil
}

func newSwitchHarness(t *testing.T) (*DeployHandler, *orderLog, func(any)) {
	t.Helper()
	h, log, db := newSwapHarness(t)
	h.clients = stopClients{switchFake{stopFake{log: log}}}
	h.runtimeBuilder = &runtimeBuilder{}
	return h, log, func(v any) {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// Container calls would silently fail to remove a Swarm service id, leaving it serving beside the new container.
func TestSwapRemovesPreviousServiceRelease(t *testing.T) {
	h, log, create := newSwitchHarness(t)
	app := &models.Application{Name: "web", Image: "nginx", Tag: "1", RuntimeKind: models.RuntimeContainer}
	create(app)
	create(&models.Release{ApplicationID: app.ID, Version: 1, ContainerID: "svc-1", Active: true, RuntimeKind: models.RuntimeService})
	dep := &models.Deployment{ApplicationID: app.ID, Strategy: models.DeployRolling}
	create(dep)

	h.swapAndRelease(app, dep, "nginx:2", "ctr-2")

	if want := []string{"sync ctr-2", "service-rm svc-1"}; !reflect.DeepEqual(log.steps, want) {
		t.Fatalf("steps = %v, want %v", log.steps, want)
	}
	if active, _ := h.releases.FindActive(app.ID); active == nil || active.RuntimeKind != models.RuntimeContainer {
		t.Fatalf("active release = %+v, want a container release", active)
	}
}

// Container -> service: the old container is retired once the route points at the service.
func TestServiceReleaseRetiresPreviousContainer(t *testing.T) {
	h, log, create := newSwitchHarness(t)
	app := &models.Application{Name: "web", Image: "nginx", Tag: "1", RuntimeKind: models.RuntimeService, Replicas: 2}
	create(app)
	create(&models.Release{ApplicationID: app.ID, Version: 1, ContainerID: "ctr-1", Active: true, RuntimeKind: models.RuntimeContainer})
	dep := &models.Deployment{ApplicationID: app.ID, Strategy: models.DeployRolling}
	create(dep)

	h.releaseService(app, dep, "nginx:2", "svc-2")

	if want := []string{"sync svc-2", "stop ctr-1"}; !reflect.DeepEqual(log.steps, want) {
		t.Fatalf("steps = %v, want %v", log.steps, want)
	}
}

// A service updated in place keeps its service; nothing is retired.
func TestServiceReleaseKeepsServiceOnRedeploy(t *testing.T) {
	h, log, create := newSwitchHarness(t)
	app := &models.Application{Name: "web", Image: "nginx", Tag: "1", RuntimeKind: models.RuntimeService}
	create(app)
	create(&models.Release{ApplicationID: app.ID, Version: 1, ContainerID: "svc-1", Active: true, RuntimeKind: models.RuntimeService})
	dep := &models.Deployment{ApplicationID: app.ID, Strategy: models.DeployRolling}
	create(dep)

	h.releaseService(app, dep, "nginx:2", "svc-1")

	if want := []string{"sync svc-1"}; !reflect.DeepEqual(log.steps, want) {
		t.Fatalf("steps = %v, want %v", log.steps, want)
	}
}

// A failed rolling switch leaves the old release serving, so the app's runtime goes back to match it.
func TestFailedRollingSwitchRevertsRuntime(t *testing.T) {
	h, _, create := newSwitchHarness(t)
	app := &models.Application{Name: "web", Image: "nginx", Tag: "1", RuntimeKind: models.RuntimeService}
	create(app)
	rel := &models.Release{ApplicationID: app.ID, Version: 1, ContainerID: "ctr-1", Active: true, RuntimeKind: models.RuntimeContainer}
	create(rel)
	if err := h.apps.SetCurrentRelease(app.ID, rel.ID, models.AppStatusRunning); err != nil {
		t.Fatal(err)
	}
	dep := &models.Deployment{ApplicationID: app.ID, Strategy: models.DeployRolling}
	create(dep)

	_ = h.fail(dep, context.DeadlineExceeded)

	if got, _ := h.apps.FindByID(app.ID); got.RuntimeKind != models.RuntimeContainer {
		t.Fatalf("runtime = %q, want container", got.RuntimeKind)
	}
}

// After a recreate switch the old workload is gone; reverting would point at nothing.
func TestFailedRecreateSwitchKeepsRuntime(t *testing.T) {
	h, _, create := newSwitchHarness(t)
	app := &models.Application{Name: "web", Image: "nginx", Tag: "1", RuntimeKind: models.RuntimeService}
	create(app)
	create(&models.Release{ApplicationID: app.ID, Version: 1, ContainerID: "ctr-1", Active: true, RuntimeKind: models.RuntimeContainer})
	dep := &models.Deployment{ApplicationID: app.ID, Strategy: models.DeployRecreate}
	create(dep)

	_ = h.fail(dep, context.DeadlineExceeded)

	if got, _ := h.apps.FindByID(app.ID); got.RuntimeKind != models.RuntimeService {
		t.Fatalf("runtime = %q, want service", got.RuntimeKind)
	}
}
