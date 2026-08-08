// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	imagesvc "github.com/miabi-io/miabi/internal/services/image"
	"github.com/miabi-io/runner/proto"
)

type fakeStore struct {
	runUpdates  int
	stepStatus  map[int]models.PipelineRunStatus
	stepLogs    map[int]string
	runImageSet uint
}

func newFakeStore() *fakeStore {
	return &fakeStore{stepStatus: map[int]models.PipelineRunStatus{}, stepLogs: map[int]string{}}
}

func (f *fakeStore) UpdateRun(*models.PipelineRun) error { f.runUpdates++; return nil }
func (f *fakeStore) UpdateStep(s *models.PipelineStepRun) error {
	f.stepStatus[s.Ordinal] = s.Status
	if s.Logs != "" {
		f.stepLogs[s.Ordinal] = s.Logs
	}
	return nil
}
func (f *fakeStore) SetRunImage(_, imageID uint) error { f.runImageSet = imageID; return nil }

type fakeImages struct{ last imagesvc.RecordInput }

func (f *fakeImages) Record(in imagesvc.RecordInput) (*models.Image, error) {
	f.last = in
	return &models.Image{ID: 99, Digest: in.Digest}, nil
}

type fakeBus struct{ events []eventbus.Event }

func (f *fakeBus) Publish(_ string, e eventbus.Event) { f.events = append(f.events, e) }

type fakeDeployer struct {
	called bool
	ref    string
	appID  uint
}

func (f *fakeDeployer) DeployByDigest(_ *models.PipelineRun, appID uint, imageRef string) error {
	f.called, f.appID, f.ref = true, appID, imageRef
	return nil
}

func frameStream(write func(fw *proto.FrameWriter)) *bytes.Buffer {
	var buf bytes.Buffer
	write(proto.NewFrameWriter(&buf))
	return &buf
}

func TestProcessFramesSuccess(t *testing.T) {
	store, images, bus := newFakeStore(), &fakeImages{}, &fakeBus{}
	d := NewDispatcher(nil, nil, nil, store, images, bus, 0)

	run := &models.PipelineRun{ID: 7, WorkspaceID: 42, Commit: "abc"}
	steps := []models.PipelineStepRun{{Ordinal: 0, Name: "build"}, {Ordinal: 1, Name: "deploy"}}
	meta := dispatchMeta{runnerName: "builtin", repository: "reg.example.com/ws-42/web"}

	stream := frameStream(func(fw *proto.FrameWriter) {
		_ = fw.Step(0, "running")
		_ = fw.Log(0, "building image")
		_ = fw.Result(0, "sha256:cafe", 0)
		_ = fw.Step(0, "succeeded")
		_ = fw.Step(1, "running")
		_ = fw.Step(1, "succeeded")
		_ = fw.Done("succeeded")
	})

	status, err := d.processFrames(context.Background(), stream, run, steps, meta)
	if err != nil {
		t.Fatalf("processFrames: %v", err)
	}
	if status != models.PipelineRunSucceeded || run.Status != models.PipelineRunSucceeded {
		t.Fatalf("status = %q, want succeeded", status)
	}
	if store.stepStatus[0] != models.PipelineRunSucceeded || store.stepStatus[1] != models.PipelineRunSucceeded {
		t.Errorf("step statuses = %v, want both succeeded", store.stepStatus)
	}
	// Image provenance recorded and pointed at by the run.
	if images.last.Digest != "sha256:cafe" || images.last.Runner != "builtin" || images.last.Repository != meta.repository {
		t.Errorf("image record = %+v", images.last)
	}
	if run.ImageID == nil || *run.ImageID != 99 || store.runImageSet != 99 {
		t.Errorf("run image not set: %v / %d", run.ImageID, store.runImageSet)
	}
	// Live logs, per-step transitions, and a terminal status were published.
	var sawLog, sawStep, sawStatus bool
	for _, e := range bus.events {
		if e.Type == "log" && e.Data == "building image" {
			sawLog = true
		}
		if e.Type == "step" && e.Data == "0:running" {
			sawStep = true
		}
		if e.Type == "status" && e.Data == string(models.PipelineRunSucceeded) {
			sawStatus = true
		}
	}
	if !sawLog || !sawStep || !sawStatus {
		t.Errorf("missing published events: log=%v step=%v status=%v", sawLog, sawStep, sawStatus)
	}
	// On terminal, the step's log is persisted (a tail here, since no store is
	// wired) so it survives past the live stream — not just published.
	if !strings.Contains(store.stepLogs[0], "building image") {
		t.Errorf("step 0 log not persisted, got %q", store.stepLogs[0])
	}
}

// A succeeded run that built a digest and has a deploy step triggers a
// deploy-by-digest of repo@digest.
func TestProcessFramesDeploysByDigest(t *testing.T) {
	d := NewDispatcher(nil, nil, nil, newFakeStore(), &fakeImages{}, &fakeBus{}, 0)
	dep := &fakeDeployer{}
	d.SetDeployer(dep)

	app := uint(128)
	run := &models.PipelineRun{ID: 7, WorkspaceID: 42, Commit: "abc"}
	steps := []models.PipelineStepRun{{Ordinal: 0, Uses: "build"}, {Ordinal: 1, Uses: "deploy"}}
	meta := dispatchMeta{runnerName: "builtin", repository: "reg.example.com/ws_42/app-128", appID: &app}

	stream := frameStream(func(fw *proto.FrameWriter) {
		_ = fw.Result(0, "sha256:cafe", 0)
		_ = fw.Step(0, "succeeded")
		_ = fw.Step(1, "succeeded")
		_ = fw.Done("succeeded")
	})
	if _, err := d.processFrames(context.Background(), stream, run, steps, meta); err != nil {
		t.Fatalf("processFrames: %v", err)
	}
	if !dep.called || dep.ref != "reg.example.com/ws_42/app-128@sha256:cafe" || dep.appID != 128 {
		t.Errorf("deploy = %+v", dep)
	}
}

// A build-only pipeline (no deploy step) never deploys; nor does a failed run.
func TestProcessFramesNoDeploy(t *testing.T) {
	app := uint(1)
	build := func(steps []models.PipelineStepRun, done string) *fakeDeployer {
		d := NewDispatcher(nil, nil, nil, newFakeStore(), &fakeImages{}, &fakeBus{}, 0)
		dep := &fakeDeployer{}
		d.SetDeployer(dep)
		stream := frameStream(func(fw *proto.FrameWriter) {
			_ = fw.Result(0, "sha256:x", 0)
			_ = fw.Done(done)
		})
		_, _ = d.processFrames(context.Background(), stream,
			&models.PipelineRun{ID: 1, WorkspaceID: 1}, steps, dispatchMeta{repository: "reg/app-1", appID: &app})
		return dep
	}
	if build([]models.PipelineStepRun{{Ordinal: 0, Uses: "build"}}, "succeeded").called {
		t.Error("build-only pipeline must not deploy")
	}
	if build([]models.PipelineStepRun{{Ordinal: 0, Uses: "build"}, {Ordinal: 1, Uses: "deploy"}}, "failed").called {
		t.Error("failed run must not deploy")
	}
}

func TestProcessFramesError(t *testing.T) {
	store := newFakeStore()
	d := NewDispatcher(nil, nil, nil, store, &fakeImages{}, &fakeBus{}, 0)
	run := &models.PipelineRun{ID: 8}
	stream := frameStream(func(fw *proto.FrameWriter) {
		_ = fw.Step(0, "running")
		_ = fw.Err("build backend not configured")
	})
	status, err := d.processFrames(context.Background(), stream, run, nil, dispatchMeta{})
	if err != nil {
		t.Fatalf("processFrames: %v", err)
	}
	if status != models.PipelineRunFailed || run.Status != models.PipelineRunFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if run.Error != "build backend not configured" {
		t.Errorf("run.Error = %q", run.Error)
	}
}

// A stream that ends before a terminal frame is a dead runner → an error, so the
// caller requeues rather than leaving the run hung.
func TestProcessFramesTruncatedStreamErrors(t *testing.T) {
	d := NewDispatcher(nil, nil, nil, newFakeStore(), &fakeImages{}, &fakeBus{}, 0)
	run := &models.PipelineRun{ID: 9}
	stream := frameStream(func(fw *proto.FrameWriter) { _ = fw.Step(0, "running") }) // no Done/Error
	if _, err := d.processFrames(context.Background(), stream, run, nil, dispatchMeta{}); err == nil {
		t.Fatal("want error on truncated stream, got nil")
	}
}

func TestMapStatus(t *testing.T) {
	cases := map[string]models.PipelineRunStatus{
		"running":   models.PipelineRunRunning,
		"succeeded": models.PipelineRunSucceeded,
		"canceled":  models.PipelineRunCanceled,
		"failed":    models.PipelineRunFailed,
		"weird":     models.PipelineRunFailed, // unknown → fail safe
	}
	for in, want := range cases {
		if got := mapStatus(in); got != want {
			t.Errorf("mapStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
