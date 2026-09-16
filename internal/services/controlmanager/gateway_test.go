// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// gatewayNode is a node Miabi deployed a gateway on; imported marks one it merely adopted, like the
// platform's own gateway on the manager.
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

// The platform's own gateway on the manager is adopted, not managed: the Miabi stack owns that container, so
// it is reported and never touched.
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

// A gateway that exists but is stopped serves nothing, and containers run unless-stopped — so it was stopped
// by hand or cannot start, and either way it is drift.
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
