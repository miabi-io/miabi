// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package controlmanager

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/settings"
)

type fakeEngine struct {
	docker.Client
	containers []docker.Container
	services   map[string]bool
	lists      int
}

func (f *fakeEngine) ListContainers(context.Context, bool) ([]docker.Container, error) {
	f.lists++
	return f.containers, nil
}

func (f *fakeEngine) ServiceInspect(_ context.Context, name string) (docker.ServiceStatus, error) {
	if f.services[name] {
		return docker.ServiceStatus{Name: name}, nil
	}
	return docker.ServiceStatus{}, docker.ErrNotFound
}

type fakeNodes struct {
	engines map[uint]*fakeEngine
	since   map[uint]time.Time
}

func (f *fakeNodes) For(id uint) (docker.Client, error) {
	if e, ok := f.engines[id]; ok {
		return e, nil
	}
	return nil, errors.New("node is offline")
}

func (f *fakeNodes) ConnectedSince(id uint) (time.Time, bool) {
	_, ok := f.engines[id]
	return f.since[id], ok
}

type fakeClusters map[uint]*fakeEngine

func (f fakeClusters) Manager(_ context.Context, id uint) (docker.Client, error) {
	if e, ok := f[id]; ok {
		return e, nil
	}
	return nil, errors.New("no manager of this cluster is connected")
}

type fakeApps []models.Application

func (f *fakeApps) ListReconcilable() ([]models.Application, error) {
	return append([]models.Application(nil), *f...), nil
}

type fakeReleases map[uint]string

func (f fakeReleases) FindActive(appID uint) (*models.Release, error) {
	cid, ok := f[appID]
	if !ok {
		return nil, errors.New("record not found")
	}
	return &models.Release{ApplicationID: appID, ContainerID: cid, Active: true}, nil
}

type fakeDeploys []uint

func (f *fakeDeploys) InProgressAppIDs() ([]uint, error) { return *f, nil }

type event struct {
	appID uint
	typ   models.AppEventType
	sev   models.AppEventSeverity
}

type fakeRecorder struct{ events []event }

func (f *fakeRecorder) Emit(_, appID uint, t models.AppEventType, sev models.AppEventSeverity, _ string, _ map[string]string, _ *uint) {
	f.events = append(f.events, event{appID: appID, typ: t, sev: sev})
}

type fakeSettings map[string]string

func (f fakeSettings) String(key, def string) string {
	if v, ok := f[key]; ok {
		return v
	}
	return def
}

type harness struct {
	svc      *Service
	nodes    *fakeNodes
	clusters fakeClusters
	apps     *fakeApps
	releases fakeReleases
	deploys  *fakeDeploys
	events   *fakeRecorder
	settings fakeSettings
	now      time.Time
}

// newHarness starts with node 1 reachable the way the local engine is: connected, with no connect time.
func newHarness() *harness {
	h := &harness{
		nodes:    &fakeNodes{engines: map[uint]*fakeEngine{1: {}}, since: map[uint]time.Time{}},
		clusters: fakeClusters{},
		apps:     &fakeApps{},
		releases: fakeReleases{},
		deploys:  &fakeDeploys{},
		events:   &fakeRecorder{},
		settings: fakeSettings{},
		now:      time.Date(2026, 9, 15, 3, 0, 0, 0, time.UTC),
	}
	h.svc = New(h.nodes, h.clusters, h.apps, h.releases, h.deploys, h.events, h.settings)
	h.svc.now = func() time.Time { return h.now }
	return h
}

func (h *harness) sweep(t *testing.T) Status {
	t.Helper()
	if err := h.svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(time.Minute)
	return h.svc.Status()
}

func containerApp(id, serverID uint) models.Application {
	return models.Application{
		ID: id, WorkspaceID: 1, Name: fmt.Sprintf("app-%d", id), ServerID: serverID,
		RuntimeKind: models.RuntimeContainer, Status: models.AppStatusRunning,
	}
}

func serviceApp(id, clusterID uint) models.Application {
	return models.Application{
		ID: id, WorkspaceID: 1, Name: fmt.Sprintf("svc-%d", id), ClusterID: clusterID,
		RuntimeKind: models.RuntimeService, Status: models.AppStatusRunning,
	}
}

func TestRemovedContainerIsReportedOnceConfirmed(t *testing.T) {
	h := newHarness()
	*h.apps = fakeApps{containerApp(7, 1)}
	h.releases[7] = "c7"

	st := h.sweep(t)
	if len(st.Findings) != 1 || st.Findings[0].Confirmed || len(h.events.events) != 0 {
		t.Fatalf("first sweep: findings=%+v events=%v; want one unconfirmed finding and no event", st.Findings, h.events.events)
	}
	h.sweep(t)
	st = h.sweep(t)
	if len(st.Findings) != 1 || !st.Findings[0].Confirmed {
		t.Fatalf("findings = %+v; want the app confirmed missing", st.Findings)
	}
	if f := st.Findings[0]; f.Class != drift.ClassMissing || f.Kind != kindContainer || f.OwnerID != 7 {
		t.Fatalf("finding = %+v; want app 7's container missing", f)
	}
	if len(h.events.events) != 1 || h.events.events[0] != (event{7, models.EventDriftDetected, models.SeverityWarning}) {
		t.Fatalf("events = %v; want a single drift.detected warning", h.events.events)
	}

	h.nodes.engines[1].containers = []docker.Container{{ID: "c7", State: "running"}}
	st = h.sweep(t)
	if len(st.Findings) != 0 {
		t.Fatalf("findings = %+v; want none once the container exists", st.Findings)
	}
	if len(h.events.events) != 2 || h.events.events[1] != (event{7, models.EventDriftResolved, models.SeverityInfo}) {
		t.Fatalf("events = %v; want drift.resolved after drift.detected", h.events.events)
	}
}

// A crash leaves its container behind, which is the restart policy's business and the user's, not drift.
func TestExitedContainerIsNotMissing(t *testing.T) {
	h := newHarness()
	*h.apps = fakeApps{containerApp(7, 1)}
	h.releases[7] = "c7"
	h.nodes.engines[1].containers = []docker.Container{{ID: "c7", State: "exited", ExitCode: 137}}

	h.sweep(t)
	if st := h.sweep(t); len(st.Findings) != 0 {
		t.Fatalf("findings = %+v; an exited container was reported missing", st.Findings)
	}
}

func TestUnobservableNodesAreUnknownNotMissing(t *testing.T) {
	h := newHarness()
	h.nodes.engines[3] = &fakeEngine{}
	*h.apps = fakeApps{containerApp(7, 2), containerApp(8, 3)}
	h.releases[7], h.releases[8] = "c7", "c8"

	var st Status
	for range confirmAfter + 1 {
		h.nodes.since[3] = h.now
		st = h.sweep(t)
	}
	if len(st.Findings) != 0 || len(h.events.events) != 0 {
		t.Fatalf("findings=%+v events=%v; apps on unobservable nodes were reported missing", st.Findings, h.events.events)
	}
	if len(st.Skipped) != 2 || st.Skipped[0].ID != 2 || st.Skipped[0].Reason != "offline" ||
		st.Skipped[1].ID != 3 || st.Skipped[1].Reason != "agent connected recently" {
		t.Fatalf("skipped = %+v; want node 2 offline and node 3 connected recently", st.Skipped)
	}
}

func TestSweepThatCannotSeeAnAppNeitherConfirmsNorClearsIt(t *testing.T) {
	h := newHarness()
	*h.apps = fakeApps{containerApp(7, 1)}
	h.releases[7] = "c7"
	h.sweep(t)

	engine := h.nodes.engines[1]
	delete(h.nodes.engines, 1)
	if st := h.sweep(t); len(st.Findings) != 1 || st.Findings[0].Confirmed {
		t.Fatalf("findings = %+v; an offline sweep changed the finding", st.Findings)
	}

	h.nodes.engines[1] = engine
	if st := h.sweep(t); len(st.Findings) != 1 || !st.Findings[0].Confirmed {
		t.Fatalf("findings = %+v; want it confirmed by the next sweep that sees the node", st.Findings)
	}
}

func TestAppWithADeployUnderWayIsNotObserved(t *testing.T) {
	h := newHarness()
	*h.apps = fakeApps{containerApp(7, 1)}
	h.releases[7] = "c7"
	h.sweep(t)

	*h.deploys = fakeDeploys{7}
	h.sweep(t)
	if st := h.sweep(t); len(st.Findings) != 0 || len(h.events.events) != 0 {
		t.Fatalf("findings=%+v events=%v; an app being deployed was reported missing", st.Findings, h.events.events)
	}
}

func TestServiceAppIsJudgedByItsClusterManager(t *testing.T) {
	h := newHarness()
	gone, live, unreachable := serviceApp(7, 5), serviceApp(8, 5), serviceApp(9, 6)
	h.clusters[5] = &fakeEngine{services: map[string]bool{node.AppAlias(&live): true}}
	*h.apps = fakeApps{gone, live, unreachable}

	h.sweep(t)
	st := h.sweep(t)
	if len(st.Findings) != 1 || st.Findings[0].OwnerID != 7 || st.Findings[0].Kind != kindService || !st.Findings[0].Confirmed {
		t.Fatalf("findings = %+v; want only app 7's service confirmed missing", st.Findings)
	}
	if len(st.Skipped) != 1 || st.Skipped[0].Scope != "cluster" || st.Skipped[0].ID != 6 {
		t.Fatalf("skipped = %+v; want cluster 6, whose manager is unreachable", st.Skipped)
	}
}

func TestOffModeObservesNothing(t *testing.T) {
	h := newHarness()
	*h.apps = fakeApps{containerApp(7, 1)}
	h.releases[7] = "c7"
	h.sweep(t)

	h.settings[settings.KeyControlManagerMode] = "off"
	h.sweep(t)
	st := h.sweep(t)
	if st.Mode != ModeOff || len(st.Findings) != 0 || st.LastSweepAt != nil {
		t.Fatalf("status = %+v; want off with nothing reported", st)
	}
	if lists := h.nodes.engines[1].lists; lists != 1 || len(h.events.events) != 0 {
		t.Fatalf("lists=%d events=%v; off mode still observed", lists, h.events.events)
	}
}
