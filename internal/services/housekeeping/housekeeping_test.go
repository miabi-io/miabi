// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import (
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// --- fakes ---

// fakeDocker implements docker.Client; only the methods the housekeeping service
// touches are overridden. Anything else panics (embedded nil interface), which
// keeps the tests honest about what the service actually calls.
type fakeDocker struct {
	docker.Client
	containers []docker.Container
	configs    []docker.ConfigInfo
	volumes    []docker.Volume
	images     []docker.Image
	disk       docker.DiskUsage

	removedContainers []string
	removedVolumes    []string
	pruneImagesCalled bool
	pruneImagesOpts   docker.PruneImagesOptions
	pruneCacheCalled  bool
}

func (f *fakeDocker) ListContainers(context.Context, bool) ([]docker.Container, error) {
	return f.containers, nil
}
func (f *fakeDocker) ListVolumes(context.Context) ([]docker.Volume, error) { return f.volumes, nil }
func (f *fakeDocker) ListManagedConfigs(context.Context) ([]docker.ConfigInfo, error) {
	return f.configs, nil
}
func (f *fakeDocker) ListImages(context.Context) ([]docker.Image, error)  { return f.images, nil }
func (f *fakeDocker) DiskUsage(context.Context) (docker.DiskUsage, error) { return f.disk, nil }
func (f *fakeDocker) RemoveContainer(_ context.Context, id string, _ bool) error {
	f.removedContainers = append(f.removedContainers, id)
	return nil
}
func (f *fakeDocker) RemoveVolume(_ context.Context, name string, _ bool) error {
	f.removedVolumes = append(f.removedVolumes, name)
	return nil
}
func (f *fakeDocker) PruneImages(_ context.Context, opts docker.PruneImagesOptions) (docker.PruneReport, error) {
	f.pruneImagesCalled = true
	f.pruneImagesOpts = opts
	return docker.PruneReport{ItemsDeleted: []string{"sha256:dead"}, SpaceReclaimed: 1024}, nil
}
func (f *fakeDocker) PruneBuildCache(context.Context) (docker.PruneReport, error) {
	f.pruneCacheCalled = true
	return docker.PruneReport{SpaceReclaimed: 2048}, nil
}

type fakeClients struct{ dc docker.Client }

func (f fakeClients) For(uint) (docker.Client, error) { return f.dc, nil }

type fakeApps struct{ apps []models.Application }

func (f fakeApps) ListByServer(uint) ([]models.Application, error) { return f.apps, nil }

// newTestService builds a Service with injected fakes (same package, so the
// unexported fields are reachable).
func newTestService(dc docker.Client, apps []models.Application, exists recordExister) *Service {
	return &Service{clients: fakeClients{dc: dc}, apps: fakeApps{apps: apps}, exists: exists}
}

// existsSet returns a recordExister backed by a set of "<kind>:<id>" keys that
// are considered present; anything else reads as gone (orphaned).
func existsSet(present ...string) recordExister {
	set := map[string]bool{}
	for _, k := range present {
		set[k] = true
	}
	return func(kind string, id uint) (bool, error) {
		return set[key(kind, id)], nil
	}
}

func key(kind string, id uint) string {
	return kind + ":" + itoa(id)
}
func itoa(n uint) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func cont(id, name, image string, labels map[string]string) docker.Container {
	return docker.Container{ID: id, Names: []string{"/" + name}, Image: image, State: "running", Labels: labels}
}

// --- drift analyzer ---

func TestAnalyzeDrift(t *testing.T) {
	dc := &fakeDocker{
		containers: []docker.Container{
			cont("c-app42", "good-app", "nginx", map[string]string{labelApp: "42", labelStack: "9"}),    // managed, record exists
			cont("c-app99", "ghost-app", "redis", map[string]string{labelApp: "99"}),                    // managed, record gone → orphan
			cont("c-gw", "goma", "jkaninda/goma-gateway", map[string]string{labelRole: "node-gateway"}), // infra → skip
			cont("c-hand", "manual", "busybox", nil),                                                    // unmanaged → untracked
		},
		volumes: []docker.Volume{
			{Name: "vol-ghost", Labels: map[string]string{labelVolume: "7"}}, // gone → orphan
			{Name: "vol-live", Labels: map[string]string{labelVolume: "3"}},  // exists → keep
			{Name: "vol-hand", Labels: nil},                                  // unmanaged → skip
		},
	}
	apps := []models.Application{
		{ID: 42, Name: "good-app", Status: models.AppStatusRunning},    // has a live container
		{ID: 50, Name: "missing-app", Status: models.AppStatusRunning}, // no live container → missing
		{ID: 60, Name: "stopped-app", Status: models.AppStatusStopped}, // not expected live → skip
	}
	exists := existsSet("app:42", "volume:3")

	s := newTestService(dc, apps, exists)
	drift, err := s.analyzeDrift(context.Background(), dc, 1)
	if err != nil {
		t.Fatalf("analyzeDrift: %v", err)
	}

	if len(drift.Orphans) != 2 {
		t.Fatalf("want 2 orphans, got %d: %+v", len(drift.Orphans), drift.Orphans)
	}
	assertHasOrphan(t, drift.Orphans, "container", "c-app99")
	assertHasOrphan(t, drift.Orphans, "volume", "vol-ghost")

	if len(drift.Untracked) != 1 || drift.Untracked[0].Ref != "c-hand" {
		t.Fatalf("want 1 untracked (c-hand), got %+v", drift.Untracked)
	}
	if len(drift.Missing) != 1 || drift.Missing[0].OwnerID != 50 {
		t.Fatalf("want 1 missing (app 50), got %+v", drift.Missing)
	}
}

// The gateway and a managed-but-live resource must never be classified as orphans.
func TestAnalyzeDrift_NeverFlagsInfraOrLive(t *testing.T) {
	dc := &fakeDocker{
		containers: []docker.Container{
			cont("c-gw", "goma", "goma", map[string]string{labelRole: "node-gateway"}),
			cont("c-redis", "redis", "redis", map[string]string{labelRole: "node-gateway-redis"}),
			cont("c-job", "job", "busybox", map[string]string{labelJob: "5", labelApp: "42"}),
			cont("c-live", "live", "nginx", map[string]string{labelApp: "42"}),
		},
	}
	s := newTestService(dc, nil, existsSet("app:42"))
	drift, err := s.analyzeDrift(context.Background(), dc, 1)
	if err != nil {
		t.Fatalf("analyzeDrift: %v", err)
	}
	if len(drift.Orphans) != 0 {
		t.Fatalf("infra/job/live must never be orphans, got %+v", drift.Orphans)
	}
}

// --- apply safety ---

// Apply must remove only resources that re-confirm as orphans; a crafted ref to
// a managed-but-live container is silently ignored, never removed.
func TestApply_OnlyRemovesConfirmedOrphans(t *testing.T) {
	dc := &fakeDocker{
		containers: []docker.Container{
			cont("c-orphan", "ghost", "redis", map[string]string{labelApp: "99"}), // gone → orphan
			cont("c-live", "live", "nginx", map[string]string{labelApp: "42"}),    // exists → not orphan
		},
	}
	s := newTestService(dc, nil, existsSet("app:42"))

	sel := Selection{Orphans: []ResourceRef{
		{Kind: "container", Ref: "c-orphan"}, // legitimately an orphan
		{Kind: "container", Ref: "c-live"},   // managed + live: must be refused
		{Kind: "container", Ref: "c-gw"},     // not present at all: ignored
	}}
	res, err := s.Apply(context.Background(), 1, sel)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(dc.removedContainers) != 1 || dc.removedContainers[0] != "c-orphan" {
		t.Fatalf("only the confirmed orphan may be removed, removed: %v", dc.removedContainers)
	}
	if len(res.OrphansRemoved) != 1 {
		t.Fatalf("want 1 orphan removed in result, got %+v", res.OrphansRemoved)
	}
}

func TestApply_Reclaim(t *testing.T) {
	dc := &fakeDocker{}
	s := newTestService(dc, nil, existsSet())
	res, err := s.Apply(context.Background(), 1, Selection{
		Reclaim: ReclaimSelection{DanglingImages: true, BuildCache: true},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !dc.pruneImagesCalled || !dc.pruneImagesOpts.Dangling {
		t.Fatalf("dangling image prune must be requested with Dangling=true, got called=%v opts=%+v", dc.pruneImagesCalled, dc.pruneImagesOpts)
	}
	if !dc.pruneCacheCalled {
		t.Fatal("build cache prune must run")
	}
	if res.ImagesBytes != 1024 || res.BuildCacheBytes != 2048 {
		t.Fatalf("reclaimed bytes not reported: %+v", res)
	}
}

// Plan and Apply must agree on what gets removed (dry-run parity).
func TestPlan_MatchesApply(t *testing.T) {
	build := func() *fakeDocker {
		return &fakeDocker{
			containers: []docker.Container{
				cont("c-orphan", "ghost", "redis", map[string]string{labelApp: "99"}),
			},
			images: []docker.Image{{ID: "img1", Size: 500, Dangling: true}},
		}
	}
	sel := Selection{
		Reclaim: ReclaimSelection{DanglingImages: true},
		Orphans: []ResourceRef{{Kind: "container", Ref: "c-orphan"}},
	}

	planSvc := newTestService(build(), nil, existsSet())
	plan, err := planSvc.Plan(context.Background(), 1, sel)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Orphans) != 1 || plan.Orphans[0].Ref != "c-orphan" {
		t.Fatalf("plan should preview the orphan, got %+v", plan.Orphans)
	}
	if plan.DanglingImages.Count != 1 || plan.DanglingImages.Bytes != 500 {
		t.Fatalf("plan should preview dangling images, got %+v", plan.DanglingImages)
	}

	applyDC := build()
	applySvc := newTestService(applyDC, nil, existsSet())
	res, err := applySvc.Apply(context.Background(), 1, sel)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.OrphansRemoved) != len(plan.Orphans) {
		t.Fatalf("dry-run parity broken: plan removed %d, apply removed %d", len(plan.Orphans), len(res.OrphansRemoved))
	}
}

func assertHasOrphan(t *testing.T, orphans []DriftItem, kind, ref string) {
	t.Helper()
	for _, o := range orphans {
		if o.Kind == kind && o.Ref == ref {
			if o.Class != ClassOrphan || o.Action != ActionRemove {
				t.Fatalf("orphan %s/%s has wrong class/action: %+v", kind, ref, o)
			}
			return
		}
	}
	t.Fatalf("expected orphan %s/%s not found in %+v", kind, ref, orphans)
}
