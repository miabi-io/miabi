// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package controlmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	volumes    []docker.Volume
	services   map[string]bool
	lists      int
}

func (f *fakeEngine) ListContainers(context.Context, bool) ([]docker.Container, error) {
	f.lists++
	return f.containers, nil
}

func (f *fakeEngine) ListVolumes(context.Context) ([]docker.Volume, error) { return f.volumes, nil }

func (f *fakeEngine) InspectContainer(_ context.Context, id string) (docker.Container, error) {
	for _, c := range f.containers {
		if c.ID == id || (len(c.Names) > 0 && strings.TrimPrefix(c.Names[0], "/") == id) {
			return c, nil
		}
	}
	return docker.Container{}, docker.ErrNotFound
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

type fakeDeploys struct {
	inProgress []uint
	byID       map[uint]*models.Deployment
}

func (f *fakeDeploys) InProgressAppIDs() ([]uint, error) { return f.inProgress, nil }

// FindByID answers pending for a deployment it was never told about, which is what a redeploy the control
// manager just started looks like.
func (f *fakeDeploys) FindByID(id uint) (*models.Deployment, error) {
	if d, ok := f.byID[id]; ok {
		return d, nil
	}
	return &models.Deployment{ID: id, Status: models.DeploymentPending}, nil
}

type fakeVolumes struct {
	rows    []models.Volume
	adopted map[uint]string
}

func (f *fakeVolumes) ListAll() ([]models.Volume, error) {
	return append([]models.Volume(nil), f.rows...), nil
}

// SetEngineCreatedAt mirrors the repository: it only writes while the row has none.
func (f *fakeVolumes) SetEngineCreatedAt(id uint, at string) error {
	for i := range f.rows {
		if f.rows[i].ID == id && f.rows[i].EngineCreatedAt == "" {
			f.rows[i].EngineCreatedAt = at
			f.adopted[id] = at
		}
	}
	return nil
}

type fakeDatabases struct {
	rows    []models.DatabaseInstance
	adopted map[uint]string
}

func (f *fakeDatabases) ListAllInstances() ([]models.DatabaseInstance, error) {
	return append([]models.DatabaseInstance(nil), f.rows...), nil
}

func (f *fakeDatabases) SetVolumeEngineCreatedAt(id uint, at string) error {
	for i := range f.rows {
		if f.rows[i].ID == id && f.rows[i].VolumeEngineCreatedAt == "" {
			f.rows[i].VolumeEngineCreatedAt = at
			f.adopted[id] = at
		}
	}
	return nil
}

type fakeVolumeBackups map[uint][]models.VolumeBackup

func (f fakeVolumeBackups) ListByVolume(volumeID uint) ([]models.VolumeBackup, error) {
	return f[volumeID], nil
}

type fakeDBBackups map[uint][]models.DatabaseBackupSet

func (f fakeDBBackups) ListByInstance(instanceID uint) ([]models.DatabaseBackupSet, error) {
	return f[instanceID], nil
}

type event struct {
	appID      uint
	databaseID uint
	typ        models.AppEventType
	sev        models.AppEventSeverity
}

type fakeRecorder struct{ events []event }

func (f *fakeRecorder) Emit(_, appID uint, t models.AppEventType, sev models.AppEventSeverity, _ string, _ map[string]string, _ *uint) {
	f.events = append(f.events, event{appID: appID, typ: t, sev: sev})
}

func (f *fakeRecorder) EmitDatabase(_, databaseID uint, _ string, t models.AppEventType, sev models.AppEventSeverity, _ string, _ map[string]string, _ *uint) {
	f.events = append(f.events, event{databaseID: databaseID, typ: t, sev: sev})
}

type fakeSettings map[string]string

func (f fakeSettings) String(key, def string) string {
	if v, ok := f[key]; ok {
		return v
	}
	return def
}

type harness struct {
	svc           *Service
	nodes         *fakeNodes
	clusters      fakeClusters
	apps          *fakeApps
	releases      fakeReleases
	deploys       *fakeDeploys
	volumes       *fakeVolumes
	databases     *fakeDatabases
	volumeBackups fakeVolumeBackups
	dbBackups     fakeDBBackups
	events        *fakeRecorder
	settings      fakeSettings
	now           time.Time
}

// newHarness starts with node 1 reachable the way the local engine is: connected, with no connect time.
func newHarness() *harness {
	h := &harness{
		nodes:         &fakeNodes{engines: map[uint]*fakeEngine{1: {}}, since: map[uint]time.Time{}},
		clusters:      fakeClusters{},
		apps:          &fakeApps{},
		releases:      fakeReleases{},
		deploys:       &fakeDeploys{},
		volumes:       &fakeVolumes{adopted: map[uint]string{}},
		databases:     &fakeDatabases{adopted: map[uint]string{}},
		volumeBackups: fakeVolumeBackups{},
		dbBackups:     fakeDBBackups{},
		events:        &fakeRecorder{},
		settings:      fakeSettings{},
		now:           time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC),
	}
	h.svc = New(h.nodes, h.clusters, h.apps, h.releases, h.deploys, h.volumes, h.databases, h.events, h.settings)
	h.svc.SetBackups(h.volumeBackups, h.dbBackups)
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

// confirm runs enough sweeps for a finding to be reported.
func (h *harness) confirm(t *testing.T) Status {
	t.Helper()
	var st Status
	for range confirmAfter {
		st = h.sweep(t)
	}
	return st
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
	st = h.sweep(t)
	if len(st.Findings) != 1 || !st.Findings[0].Confirmed {
		t.Fatalf("findings = %+v; want the app confirmed missing", st.Findings)
	}
	if f := st.Findings[0]; f.Class != drift.ClassMissing || f.Kind != kindContainer || f.OwnerID != 7 {
		t.Fatalf("finding = %+v; want app 7's container missing", f)
	}
	if len(h.events.events) != 1 || h.events.events[0] != (event{appID: 7, typ: models.EventDriftDetected, sev: models.SeverityWarning}) {
		t.Fatalf("events = %v; want a single drift.detected warning", h.events.events)
	}

	h.nodes.engines[1].containers = []docker.Container{{ID: "c7", State: "running"}}
	st = h.sweep(t)
	if len(st.Findings) != 0 {
		t.Fatalf("findings = %+v; want none once the container exists", st.Findings)
	}
	if len(h.events.events) != 2 || h.events.events[1].typ != models.EventDriftResolved {
		t.Fatalf("events = %v; want drift.resolved after drift.detected", h.events.events)
	}
}

// A crash leaves its container behind, which is the restart policy's business and the user's, not drift.
func TestExitedContainerIsNotMissing(t *testing.T) {
	h := newHarness()
	*h.apps = fakeApps{containerApp(7, 1)}
	h.releases[7] = "c7"
	h.nodes.engines[1].containers = []docker.Container{{ID: "c7", State: "exited", ExitCode: 137}}

	if st := h.confirm(t); len(st.Findings) != 0 {
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

	h.deploys.inProgress = []uint{7}
	if st := h.confirm(t); len(st.Findings) != 0 || len(h.events.events) != 0 {
		t.Fatalf("findings=%+v events=%v; an app being deployed was reported missing", st.Findings, h.events.events)
	}
}

func TestServiceAppIsJudgedByItsClusterManager(t *testing.T) {
	h := newHarness()
	gone, live, unreachable := serviceApp(7, 5), serviceApp(8, 5), serviceApp(9, 6)
	h.clusters[5] = &fakeEngine{services: map[string]bool{node.AppAlias(&live): true}}
	*h.apps = fakeApps{gone, live, unreachable}

	st := h.confirm(t)
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

// appVolume is a volume row plus an app that mounts it, the pairing the data-loss guard is about.
func appVolume(h *harness, appID, volumeID uint, dockerName, engineCreatedAt string) {
	app := containerApp(appID, 1)
	app.Mounts = []models.AppMount{{VolumeID: volumeID, DockerName: dockerName, Path: "/data"}}
	*h.apps = append(*h.apps, app)
	h.releases[appID] = fmt.Sprintf("c%d", appID)
	h.nodes.engines[1].containers = append(h.nodes.engines[1].containers, docker.Container{ID: fmt.Sprintf("c%d", appID), State: "running"})
	h.volumes.rows = append(h.volumes.rows, models.Volume{
		ID: volumeID, WorkspaceID: 1, Name: dockerName, DockerName: dockerName, ServerID: 1,
		Driver: models.VolumeDriverLocal, EngineCreatedAt: engineCreatedAt,
	})
}

func TestMissingVolumeBlocksTheAppsThatMountIt(t *testing.T) {
	h := newHarness()
	appVolume(h, 7, 3, "mb-vol-1-data", "2026-09-01T10:00:00Z")

	st := h.confirm(t)
	if len(st.Findings) != 1 {
		t.Fatalf("findings = %+v; want the volume reported missing", st.Findings)
	}
	f := st.Findings[0]
	if f.Kind != kindVolume || f.Class != drift.ClassMissing || f.OwnerKind != drift.OwnerVolume || f.OwnerID != 3 {
		t.Fatalf("finding = %+v; want volume 3 missing", f)
	}
	// Recreating it would give an empty volume, so the only way back is a restore.
	if f.Action != drift.ActionRestore {
		t.Fatalf("action = %q; want %q", f.Action, drift.ActionRestore)
	}
	if len(st.Blocked) != 1 || st.Blocked[0].AppID != 7 {
		t.Fatalf("blocked = %+v; want app 7 blocked", st.Blocked)
	}
	if reason, ok := h.svc.BlockedRedeploy(7); !ok || reason == "" {
		t.Fatalf("BlockedRedeploy(7) = (%q, %v); want a reason", reason, ok)
	}
	// The event lands on the timeline of the app that mounts it, since a volume has none of its own.
	if len(h.events.events) != 1 || h.events.events[0] != (event{appID: 7, typ: models.EventDriftDetected, sev: models.SeverityError}) {
		t.Fatalf("events = %v; want one error on app 7's timeline", h.events.events)
	}

	h.nodes.engines[1].volumes = []docker.Volume{{Name: "mb-vol-1-data", CreatedAt: "2026-09-01T10:00:00Z"}}
	st = h.sweep(t)
	if len(st.Findings) != 0 || len(st.Blocked) != 0 {
		t.Fatalf("findings=%+v blocked=%+v; want both cleared once the volume is back", st.Findings, st.Blocked)
	}
	if _, ok := h.svc.BlockedRedeploy(7); ok {
		t.Fatal("the app is still blocked after its volume came back")
	}
}

// A volume deleted and recreated by hand keeps its name and its row: only the engine's creation
// timestamp gives it away.
func TestRecreatedVolumeReadsAsReplaced(t *testing.T) {
	h := newHarness()
	appVolume(h, 7, 3, "mb-vol-1-data", "2026-09-01T10:00:00Z")
	h.nodes.engines[1].volumes = []docker.Volume{{Name: "mb-vol-1-data", CreatedAt: "2026-09-16T02:00:00Z"}}

	st := h.confirm(t)
	if len(st.Findings) != 1 || st.Findings[0].Class != drift.ClassReplaced {
		t.Fatalf("findings = %+v; want the volume reported replaced", st.Findings)
	}
	if len(st.Blocked) != 1 || st.Blocked[0].AppID != 7 {
		t.Fatalf("blocked = %+v; want app 7 blocked", st.Blocked)
	}
	// The row keeps the timestamp it was created with, so the loss stays visible.
	if got := h.volumes.rows[0].EngineCreatedAt; got != "2026-09-01T10:00:00Z" {
		t.Fatalf("engine_created_at = %q; a replaced volume must not adopt the new timestamp", got)
	}
}

// Volumes created before Miabi recorded a timestamp adopt the engine's, rather than reading as replaced.
func TestVolumeWithNoRecordedTimestampAdoptsTheEngines(t *testing.T) {
	h := newHarness()
	appVolume(h, 7, 3, "mb-vol-1-data", "")
	h.nodes.engines[1].volumes = []docker.Volume{{Name: "mb-vol-1-data", CreatedAt: "2026-05-04T09:00:00Z"}}

	st := h.confirm(t)
	if len(st.Findings) != 0 || len(h.events.events) != 0 {
		t.Fatalf("findings=%+v events=%v; an unrecorded volume was reported as drift", st.Findings, h.events.events)
	}
	if h.volumes.adopted[3] != "2026-05-04T09:00:00Z" {
		t.Fatalf("adopted = %v; want the engine's timestamp recorded once", h.volumes.adopted)
	}
}

func TestMissingDatabaseVolumeIsReportedOnTheInstance(t *testing.T) {
	h := newHarness()
	h.databases.rows = []models.DatabaseInstance{{
		ID: 5, WorkspaceID: 1, Name: "main", ServerID: 1,
		VolumeName: "mb-db-x-5-data", VolumeEngineCreatedAt: "2026-09-01T10:00:00Z",
	}}

	st := h.confirm(t)
	if len(st.Findings) != 1 || st.Findings[0].OwnerKind != drift.OwnerDatabase || st.Findings[0].OwnerID != 5 {
		t.Fatalf("findings = %+v; want instance 5's data volume missing", st.Findings)
	}
	if len(h.events.events) != 1 || h.events.events[0] != (event{databaseID: 5, typ: models.EventDriftDetected, sev: models.SeverityError}) {
		t.Fatalf("events = %v; want one error on the instance's timeline", h.events.events)
	}
}

// A volume on a node that cannot be reached is unknown, and must never block the apps that mount it.
func TestVolumeOnAnOfflineNodeDoesNotBlockAnything(t *testing.T) {
	h := newHarness()
	appVolume(h, 7, 3, "mb-vol-1-data", "2026-09-01T10:00:00Z")
	h.volumes.rows[0].ServerID = 2 // a node with no engine

	st := h.confirm(t)
	if len(st.Findings) != 0 || len(st.Blocked) != 0 || len(h.events.events) != 0 {
		t.Fatalf("findings=%+v blocked=%+v events=%v; an unreachable node was read as data loss", st.Findings, st.Blocked, h.events.events)
	}
	if len(st.Skipped) != 1 || st.Skipped[0].ID != 2 {
		t.Fatalf("skipped = %+v; want node 2 reported unobserved", st.Skipped)
	}
}

// A lost volume is never restored by Miabi: the report names the backup to restore, and the app that
// mounts it is told it needs a manual restore rather than a redeploy.
func TestLostVolumeSuggestsTheLatestBackupAndNeverRedeploys(t *testing.T) {
	h := newHarness()
	appVolume(h, 7, 3, "mb-vol-1-data", "2026-09-01T10:00:00Z")
	// Its container is gone too, so without the guard the app would read as "redeploy me".
	h.nodes.engines[1].containers = nil
	taken := time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)
	h.volumeBackups[3] = []models.VolumeBackup{
		{ID: 41, VolumeID: 3, Status: models.BackupRunning, CreatedAt: h.now},
		{ID: 40, VolumeID: 3, Status: models.BackupCompleted, Filename: "vol-3.tar.gz", SizeBytes: 2048, CreatedAt: taken},
	}

	st := h.confirm(t)
	var volume, container *Finding
	for i := range st.Findings {
		switch st.Findings[i].Kind {
		case kindVolume:
			volume = &st.Findings[i]
		case kindContainer:
			container = &st.Findings[i]
		}
	}
	if volume == nil || container == nil {
		t.Fatalf("findings = %+v; want both the volume and the container reported", st.Findings)
	}
	// The newest COMPLETED backup, not the one still running.
	if volume.Restore == nil || !volume.Restore.Available || volume.Restore.BackupID != 40 || !volume.Restore.CreatedAt.Equal(taken) {
		t.Fatalf("restore = %+v; want the completed backup 40 suggested", volume.Restore)
	}
	if container.Action != drift.ActionRestore || !container.Blocked || container.BlockedReason == "" {
		t.Fatalf("container finding = %+v; want it blocked and pointed at a restore, not a redeploy", container)
	}
	if len(st.Blocked) != 1 || st.Blocked[0].AppID != 7 || st.Blocked[0].Name == "" {
		t.Fatalf("blocked = %+v; want app 7 named", st.Blocked)
	}
}

// No backup is the worst case, and the report has to say so rather than leave it to be discovered.
func TestLostVolumeWithNoBackupSaysSo(t *testing.T) {
	h := newHarness()
	appVolume(h, 7, 3, "mb-vol-1-data", "2026-09-01T10:00:00Z")
	h.volumeBackups[3] = []models.VolumeBackup{{ID: 41, VolumeID: 3, Status: models.BackupFailed}}

	st := h.confirm(t)
	if len(st.Findings) != 1 || st.Findings[0].Restore == nil || st.Findings[0].Restore.Available {
		t.Fatalf("findings = %+v; want the volume reported with no backup available", st.Findings)
	}
}

func TestLostDatabaseVolumeSuggestsARecoveryPoint(t *testing.T) {
	h := newHarness()
	h.databases.rows = []models.DatabaseInstance{{
		ID: 5, WorkspaceID: 1, Name: "main", ServerID: 1,
		VolumeName: "mb-db-x-5-data", VolumeEngineCreatedAt: "2026-09-01T10:00:00Z",
	}}
	h.dbBackups[5] = []models.DatabaseBackupSet{
		{ID: 9, InstanceID: 5, Ref: "mbdb_5_20260915T010000Z", Status: models.BackupCompleted},
	}

	st := h.confirm(t)
	if len(st.Findings) != 1 {
		t.Fatalf("findings = %+v; want the instance's data volume reported", st.Findings)
	}
	r := st.Findings[0].Restore
	if r == nil || !r.Available || r.From != "recovery-point" || r.Ref != "mbdb_5_20260915T010000Z" {
		t.Fatalf("restore = %+v; want recovery point mbdb_5_20260915T010000Z suggested", r)
	}
}

// A host-driver volume is a bind to an operator-managed path; there is no Docker volume to inspect.
func TestHostVolumesAreNotWatched(t *testing.T) {
	h := newHarness()
	h.volumes.rows = []models.Volume{{
		ID: 3, WorkspaceID: 1, Name: "shared", DockerName: "mb-vol-1-shared", ServerID: 1,
		Driver: models.VolumeDriverHost, HostPath: "/mnt/shared",
	}}

	if st := h.confirm(t); len(st.Findings) != 0 {
		t.Fatalf("findings = %+v; a host bind was reported as a missing volume", st.Findings)
	}
}
