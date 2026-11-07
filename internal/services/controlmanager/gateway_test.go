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
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
)

type fakeServers []models.Server

func (f *fakeServers) List(context.Context) ([]models.Server, error) { return *f, nil }

type fakeEnsurer struct {
	calls []uint
	err   error
}

func (f *fakeEnsurer) EnsureGateway(_ context.Context, serverID uint) error {
	f.calls = append(f.calls, serverID)
	return f.err
}

func gatewayNode(id uint, name string, imported bool) models.Server {
	deployed := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	srv := models.Server{
		ID: id, Name: name, Connectivity: models.ConnectivityEdgeGateway, GatewayDeployedAt: &deployed,
	}
	if imported {
		srv.GatewayImported = true
		srv.GatewayContainer = edgegateway.CentralContainerName
	}
	return srv
}

func (h *harness) watchingGateways(servers ...models.Server) (*fakeServers, *fakeEnsurer) {
	list, ensurer := fakeServers(servers), &fakeEnsurer{}
	h.svc.SetGateways(&list, ensurer)
	return &list, ensurer
}

func TestMissingNodeGatewayIsReportedAndPutBack(t *testing.T) {
	h := newHarness()
	e := h.enforcing()
	_, ensurer := h.watchingGateways(gatewayNode(1, "edge-1", false))

	st := h.confirm(t)
	if len(st.Findings) != 1 {
		t.Fatalf("findings = %+v; want the node's gateway reported", st.Findings)
	}
	f := st.Findings[0]
	if f.Kind != kindGateway || f.OwnerKind != drift.OwnerNode || f.OwnerID != 1 || f.Action != drift.ActionRedeploy {
		t.Fatalf("finding = %+v; want node 1's gateway missing and redeployable", f)
	}
	if len(ensurer.calls) != 1 || ensurer.calls[0] != 1 {
		t.Fatalf("ensure calls = %v; want the gateway put back on node 1", ensurer.calls)
	}
	// A gateway belongs to a node, not a workspace, so it has no app timeline to write to.
	if len(h.events.events) != 0 {
		t.Fatalf("events = %v; want none for a gateway", h.events.events)
	}
	// Nothing was redeployed as an app.
	if len(e.redeploy.calls) != 0 {
		t.Fatalf("redeploys = %+v; want none", e.redeploy.calls)
	}
}

// The Miabi stack owns that container, so it is reported and never touched.
func TestImportedGatewayIsReportedButNeverTouched(t *testing.T) {
	h := newHarness()
	h.enforcing()
	_, ensurer := h.watchingGateways(gatewayNode(1, "manager", true))

	st := h.confirm(t)
	if len(st.Findings) != 1 || st.Findings[0].Action != drift.ActionNone {
		t.Fatalf("findings = %+v; want it reported with nothing for Miabi to do", st.Findings)
	}
	if len(ensurer.calls) != 0 {
		t.Fatalf("ensure calls = %v; an imported gateway must never be recreated", ensurer.calls)
	}
}

func TestRunningGatewayIsNotDrift(t *testing.T) {
	h := newHarness()
	h.enforcing()
	_, ensurer := h.watchingGateways(gatewayNode(1, "edge-1", false))
	h.nodes.engines[1].containers = []docker.Container{
		{ID: "gw", Names: []string{"/" + edgegateway.ContainerName}, State: "running"},
	}

	if st := h.confirm(t); len(st.Findings) != 0 {
		t.Fatalf("findings = %+v; a running gateway is not drift", st.Findings)
	}
	if len(ensurer.calls) != 0 {
		t.Fatalf("ensure calls = %v; want none", ensurer.calls)
	}
}

// Containers run unless-stopped, so a stopped gateway was stopped by hand or cannot start.
func TestStoppedGatewayIsDrift(t *testing.T) {
	h := newHarness()
	h.enforcing()
	_, ensurer := h.watchingGateways(gatewayNode(1, "edge-1", false))
	h.nodes.engines[1].containers = []docker.Container{
		{ID: "gw", Names: []string{"/" + edgegateway.ContainerName}, State: "exited"},
	}

	if st := h.confirm(t); len(st.Findings) != 1 {
		t.Fatalf("findings = %+v; want a stopped gateway reported", st.Findings)
	}
	if len(ensurer.calls) != 1 {
		t.Fatalf("ensure calls = %v; want it put back", ensurer.calls)
	}
}

// Observe mode reports gateways and touches nothing.
func TestObserveModeReportsGatewaysWithoutActing(t *testing.T) {
	h := newHarness()
	_, ensurer := h.watchingGateways(gatewayNode(1, "edge-1", false))

	st := h.confirm(t)
	if len(st.Findings) != 1 {
		t.Fatalf("findings = %+v; want the gateway reported", st.Findings)
	}
	if len(ensurer.calls) != 0 {
		t.Fatalf("ensure calls = %v; observe mode must act on nothing", ensurer.calls)
	}
}
