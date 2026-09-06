// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package stack

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
)

// fakeApp returns a preset error per app id; nil means success. It implements
// the stack.AppService interface (lifecycle methods used here, the rest are
// no-ops for these tests).
type fakeApp struct {
	errByID  map[uint]error
	stops    []uint
	waited   []uint
	marked   []uint
	deployed []uint
}

func (f *fakeApp) Start(_ context.Context, app *models.Application) (*models.Deployment, error) {
	return nil, f.errByID[app.ID]
}
func (f *fakeApp) Stop(_ context.Context, app *models.Application) error {
	f.stops = append(f.stops, app.ID)
	return f.errByID[app.ID]
}
func (f *fakeApp) Restart(_ context.Context, app *models.Application) (*models.Deployment, error) {
	return nil, f.errByID[app.ID]
}
func (f *fakeApp) WaitRunning(_ context.Context, app *models.Application) error {
	f.waited = append(f.waited, app.ID)
	return nil
}
func (f *fakeApp) Deploy(app *models.Application, _ *uint, _ string, _ models.DeployStrategy) (*models.Deployment, error) {
	f.deployed = append(f.deployed, app.ID)
	return &models.Deployment{ApplicationID: app.ID}, nil
}

// MarkRedeployRequired mirrors the real one: an app that has never been deployed
// has no stale container, so nothing is marked.
func (f *fakeApp) MarkRedeployRequired(app *models.Application) (bool, error) {
	if app.CurrentReleaseID == nil {
		return false, nil
	}
	app.RedeployRequired = true
	f.marked = append(f.marked, app.ID)
	return true, nil
}
func (f *fakeApp) Delete(_ context.Context, _ *models.Application) error { return nil }
func (f *fakeApp) Create(_ uint, _ application.CreateInput) (*models.Application, error) {
	return &models.Application{}, nil
}
func (f *fakeApp) SetEnvVar(_ uint, _, _ string, _ bool) error                { return nil }
func (f *fakeApp) AttachVolume(_ *models.Application, _ uint, _ string) error { return nil }

// fakeVols is a no-op VolumeCreator for tests.
type fakeVols struct{}

func (fakeVols) Create(_ context.Context, _, _ uint, name string, _ int64, _, _ models.Metadata) (*models.Volume, error) {
	return &models.Volume{Name: name}, nil
}

func TestApplyLifecycle_ClassifiesResults(t *testing.T) {
	fake := &fakeApp{errByID: map[uint]error{
		2: application.ErrNotDeployable,  // skipped
		3: errors.New("docker exploded"), // failed
	}}
	s := NewService(nil, nil, nil, nil, fake, fakeVols{}, nil, nil)

	apps := []models.Application{
		{ID: 1, Name: "web"},
		{ID: 2, Name: "worker"},
		{ID: 3, Name: "cache"},
	}
	results, err := s.applyLifecycle(context.Background(), apps, ActionStop, false)
	if err != nil {
		t.Fatalf("applyLifecycle: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	want := map[uint]string{1: "ok", 2: "skipped", 3: "failed"}
	for _, r := range results {
		if r.Status != want[r.AppID] {
			t.Errorf("app %d status = %q, want %q", r.AppID, r.Status, want[r.AppID])
		}
	}
	// Every app is acted on regardless of earlier failures (best-effort).
	if len(fake.stops) != 3 {
		t.Errorf("expected all 3 apps to be acted on, got %v", fake.stops)
	}
}

func TestApplyLifecycle_RollingGatesEachApp(t *testing.T) {
	fake := &fakeApp{}
	s := NewService(nil, nil, nil, nil, fake, fakeVols{}, nil, nil)
	apps := []models.Application{{ID: 1, Name: "web"}, {ID: 2, Name: "worker"}}
	if _, err := s.applyLifecycle(context.Background(), apps, ActionRestart, true); err != nil {
		t.Fatalf("applyLifecycle: %v", err)
	}
	// Rolling restart waits for readiness on each app.
	if len(fake.waited) != 2 {
		t.Errorf("expected WaitRunning called for both apps, got %v", fake.waited)
	}
}

func TestApplyLifecycle_InvalidAction(t *testing.T) {
	s := NewService(nil, nil, nil, nil, &fakeApp{}, fakeVols{}, nil, nil)
	if _, err := s.applyLifecycle(context.Background(), []models.Application{{ID: 1}}, Action("bogus"), false); !errors.Is(err, ErrInvalidAction) {
		t.Errorf("got %v, want ErrInvalidAction", err)
	}
}
