// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import (
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// reclaimDocker adds image removal and a measured volume breakdown to fakeDocker.
type reclaimDocker struct {
	*fakeDocker
	usage         []docker.VolumeUsage
	removedImages []string
}

func (r *reclaimDocker) DiskUsage(context.Context) (docker.DiskUsage, error) {
	du := r.disk
	du.VolumeItems = r.usage
	return du, nil
}

func (r *reclaimDocker) RemoveImage(_ context.Context, ref string, _ bool) error {
	r.removedImages = append(r.removedImages, ref)
	return nil
}

// staticRefs is an ImageRefSource over a fixed list.
type staticRefs []string

func (s staticRefs) ImageRefs() ([]string, error) { return []string(s), nil }

func pulled(id, tag string, size, containers int64) docker.Image {
	return docker.Image{
		ID: id, RepoTags: []string{tag}, RepoDigests: []string{"repo@sha256:" + id},
		Size: size, Containers: containers,
	}
}

func TestUnusedImages_Guard(t *testing.T) {
	imgs := []docker.Image{
		pulled("free", "nginx:1.25", 100, 0),   // nothing holds it, nothing names it → reclaimable
		pulled("inuse", "redis:7", 200, 2),     // a container holds it
		pulled("named", "postgres:16", 300, 0), // an app names it
		pulled("rollback", "app:v1", 400, 0),   // a release names it
		{ID: "dangle", Size: 500, Dangling: true},
		{ID: "local", RepoTags: []string{"my-app:1"}, Size: 40}, // built here, never pushed
	}
	referenced := map[string]bool{"postgres:16": true, "app:v1": true}

	got := unusedImages(imgs, referenced)
	if len(got) != 1 || got[0].ID != "free" {
		t.Fatalf("want only the unreferenced, re-pullable, unheld image, got %+v", got)
	}
}

// An image matched by its digest or its id is referenced just as much as one matched by its tag.
func TestUnusedImages_MatchesDigestAndID(t *testing.T) {
	imgs := []docker.Image{
		pulled("byDigest", "a:1", 10, 0),
		pulled("byID", "b:1", 20, 0),
		pulled("free", "c:1", 30, 0),
	}
	referenced := map[string]bool{"repo@sha256:byDigest": true, "byID": true}

	got := unusedImages(imgs, referenced)
	if len(got) != 1 || got[0].ID != "free" {
		t.Fatalf("digest and id references must protect an image, got %+v", got)
	}
}

// Without the guard wired there is no set of protected images, so nothing may be reclaimed.
func TestUnusedImages_NoGuardReclaimsNothing(t *testing.T) {
	if got := unusedImages([]docker.Image{pulled("free", "nginx:1.25", 100, 0)}, nil); len(got) != 0 {
		t.Fatalf("an unwired guard must reclaim nothing, got %+v", got)
	}
}

func TestUnusedVolumes_Guard(t *testing.T) {
	vols := []docker.Volume{
		{Name: "foreign"},          // hand-made, unattached → reclaimable
		{Name: "attached"},         // a container holds it
		{Name: "claimed"},          // a volume row names it
		{Name: "mb-vol-3-uploads"}, // Miabi's naming convention
		{Name: "labelled", Labels: map[string]string{labelVolume: "7"}},           // the orphan path owns it
		{Name: "infra", Labels: map[string]string{labelRole: docker.RoleGateway}}, // platform infrastructure
	}
	usage := []docker.VolumeUsage{
		{DockerName: "foreign", Bytes: 10},
		{DockerName: "attached", Bytes: 20, RefCount: 1},
		{DockerName: "claimed", Bytes: 30},
		{DockerName: "mb-vol-3-uploads", Bytes: 40},
		{DockerName: "labelled", Bytes: 50},
		{DockerName: "infra", Bytes: 60},
	}

	got, err := unusedVolumes(vols, usage, namedSet("claimed"))
	if err != nil {
		t.Fatalf("unusedVolumes: %v", err)
	}
	if len(got) != 1 || got[0].DockerName != "foreign" {
		t.Fatalf("only a foreign, unattached, unclaimed volume may be reclaimed, got %+v", got)
	}
}

// Without the volume-row lookup a volume cannot be proven unclaimed, so none is reclaimable.
func TestUnusedVolumes_NoLookupReclaimsNothing(t *testing.T) {
	got, err := unusedVolumes([]docker.Volume{{Name: "foreign"}}, []docker.VolumeUsage{{DockerName: "foreign"}}, nil)
	if err != nil {
		t.Fatalf("unusedVolumes: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("no lookup wired must reclaim nothing, got %+v", got)
	}
}

// The guarded categories are removed resource by resource, never by handing the category to the
// daemon's prune, so what the report counted is exactly what goes.
func TestApply_ReclaimsUnusedImagesAndVolumes(t *testing.T) {
	dc := &reclaimDocker{
		fakeDocker: &fakeDocker{
			images: []docker.Image{
				pulled("free", "nginx:1.25", 100, 0),
				pulled("named", "postgres:16", 300, 0),
			},
			volumes: []docker.Volume{{Name: "foreign"}, {Name: "claimed"}},
		},
		usage: []docker.VolumeUsage{{DockerName: "foreign", Bytes: 10}, {DockerName: "claimed", Bytes: 30}},
	}
	s := newTestService(dc, nil, existsSet())
	s.volumeNamed = namedSet("claimed")
	s.SetImageRefs(staticRefs{"postgres:16"})

	res, err := s.Apply(context.Background(), 1, Selection{
		Reclaim: ReclaimSelection{UnusedImages: true, UnusedVolumes: true},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(dc.removedImages) != 1 || dc.removedImages[0] != "free" {
		t.Fatalf("only the unreferenced image may be removed, removed: %v", dc.removedImages)
	}
	if len(dc.removedVolumes) != 1 || dc.removedVolumes[0] != "foreign" {
		t.Fatalf("only the foreign volume may be removed, removed: %v", dc.removedVolumes)
	}
	if dc.pruneImagesCalled {
		t.Fatal("the guarded categories must never fall back to a blanket image prune")
	}
	if res.ImagesDeleted != 1 || res.ImagesBytes != 100 || res.VolumesDeleted != 1 || res.VolumesBytes != 10 {
		t.Fatalf("result must report what went: %+v", res)
	}
}

// Dry-run parity: the preview names exactly the resources the apply removes.
func TestPlan_NamesTheGuardedResources(t *testing.T) {
	build := func() *reclaimDocker {
		return &reclaimDocker{
			fakeDocker: &fakeDocker{
				images:  []docker.Image{pulled("free", "nginx:1.25", 100, 0), pulled("named", "postgres:16", 300, 0)},
				volumes: []docker.Volume{{Name: "foreign"}},
			},
			usage: []docker.VolumeUsage{{DockerName: "foreign", Bytes: 10}},
		}
	}
	newSvc := func(dc docker.Client) *Service {
		s := newTestService(dc, nil, existsSet())
		s.volumeNamed = namedSet()
		s.SetImageRefs(staticRefs{"postgres:16"})
		return s
	}
	sel := Selection{Reclaim: ReclaimSelection{UnusedImages: true, UnusedVolumes: true}}

	plan, err := newSvc(build()).Plan(context.Background(), 1, sel)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.UnusedImageRefs) != 1 || plan.UnusedImageRefs[0] != "nginx:1.25" {
		t.Fatalf("preview must name the images, got %v", plan.UnusedImageRefs)
	}
	if len(plan.UnusedVolumeNames) != 1 || plan.UnusedVolumeNames[0] != "foreign" {
		t.Fatalf("preview must name the volumes, got %v", plan.UnusedVolumeNames)
	}
	if plan.EstimatedBytes != 110 {
		t.Fatalf("estimate must be the two categories, got %d", plan.EstimatedBytes)
	}

	res, err := newSvc(build()).Apply(context.Background(), 1, sel)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.ImagesDeleted != len(plan.UnusedImageRefs) || res.VolumesDeleted != len(plan.UnusedVolumeNames) {
		t.Fatalf("dry-run parity broken: plan %d/%d, apply %d/%d",
			len(plan.UnusedImageRefs), len(plan.UnusedVolumeNames), res.ImagesDeleted, res.VolumesDeleted)
	}
}

// Asking for unused-image reclaim on a build with no guard wired must say so rather than quietly
// reclaim nothing and report success.
func TestApply_UnusedImagesWithoutGuardErrors(t *testing.T) {
	dc := &reclaimDocker{fakeDocker: &fakeDocker{images: []docker.Image{pulled("free", "nginx:1.25", 100, 0)}}}
	s := newTestService(dc, nil, existsSet())

	res, err := s.Apply(context.Background(), 1, Selection{Reclaim: ReclaimSelection{UnusedImages: true}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(dc.removedImages) != 0 || len(res.Errors) != 1 {
		t.Fatalf("want no removal and one reported error, got removed=%v errors=%v", dc.removedImages, res.Errors)
	}
}

// The report's counts are the guarded sets, not Docker's raw idea of what is unused.
func TestAnalyze_ReclaimBreakdownIsGuarded(t *testing.T) {
	dc := &reclaimDocker{
		fakeDocker: &fakeDocker{
			images: []docker.Image{
				pulled("free", "nginx:1.25", 100, 0),
				pulled("named", "postgres:16", 300, 0),
				{ID: "dangle", Size: 500, Dangling: true},
			},
			volumes: []docker.Volume{{Name: "foreign"}, {Name: "mb-vol-3-uploads"}},
			disk:    docker.DiskUsage{BuildCache: docker.DiskUsageCategory{Count: 8, Active: 3, Reclaimable: 900}},
		},
		usage: []docker.VolumeUsage{{DockerName: "foreign", Bytes: 10}, {DockerName: "mb-vol-3-uploads", Bytes: 40}},
	}
	s := newTestService(dc, nil, existsSet())
	s.volumeNamed = namedSet()
	s.SetImageRefs(staticRefs{"postgres:16"})

	rep, err := s.Analyze(context.Background(), 1)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.Reclaim.UnusedImages != (CategoryStat{Count: 1, Bytes: 100}) {
		t.Errorf("unused images = %+v, want 1 image / 100 bytes", rep.Reclaim.UnusedImages)
	}
	if rep.Reclaim.UnusedVolumes != (CategoryStat{Count: 1, Bytes: 10}) {
		t.Errorf("unused volumes = %+v, want 1 volume / 10 bytes", rep.Reclaim.UnusedVolumes)
	}
	if rep.Reclaim.DanglingImages != (CategoryStat{Count: 1, Bytes: 500}) {
		t.Errorf("dangling images = %+v, want 1 image / 500 bytes", rep.Reclaim.DanglingImages)
	}
	if rep.Reclaim.BuildCache != (CategoryStat{Count: 5, Bytes: 900}) {
		t.Errorf("build cache = %+v, want 5 records / 900 bytes", rep.Reclaim.BuildCache)
	}
	if rep.Disk.VolumeItems != nil {
		t.Error("the report carries disk totals, not a row per volume")
	}
}

// Regression: summing each image's total size counted a shared base layer once per image carrying
// it, so the estimate promised several times the disk the apply could return.
func TestAnalyze_EstimateExcludesSharedLayers(t *testing.T) {
	shared := func(id, tag string, size, sharedSize int64) docker.Image {
		im := pulled(id, tag, size, 0)
		im.SharedSize = sharedSize
		return im
	}
	dc := &reclaimDocker{fakeDocker: &fakeDocker{images: []docker.Image{
		shared("a", "a:1", 500, 400), // 400 of it is a base layer b also carries
		shared("b", "b:1", 450, 400),
	}}}
	s := newTestService(dc, nil, existsSet())
	s.SetImageRefs(staticRefs{})

	rep, err := s.Analyze(context.Background(), 1)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.Reclaim.UnusedImages.Bytes != 150 {
		t.Fatalf("estimate = %d, want 150 (the unshared bytes, not 950)", rep.Reclaim.UnusedImages.Bytes)
	}
}
