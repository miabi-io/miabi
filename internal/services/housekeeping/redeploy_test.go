// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type redeployCall struct {
	appID  uint
	reason string
}

type fakeRedeployer struct {
	calls []redeployCall
	err   error
}

func (f *fakeRedeployer) ReconcileRedeploy(app *models.Application, reason string) (*models.Deployment, error) {
	f.calls = append(f.calls, redeployCall{appID: app.ID, reason: reason})
	if f.err != nil {
		return nil, f.err
	}
	return &models.Deployment{ID: 1, ApplicationID: app.ID}, nil
}

type fakeAppFinder map[uint]*models.Application

func (f fakeAppFinder) FindByID(id uint) (*models.Application, error) {
	if a, ok := f[id]; ok {
		return a, nil
	}
	return nil, errors.New("record not found")
}

// missingAppService builds a service whose node runs nothing, so the app it knows about reads as missing.
func missingAppService(t *testing.T, containers []docker.Container) (*Service, *fakeRedeployer) {
	t.Helper()
	svc := newTestService(
		&fakeDocker{containers: containers},
		[]models.Application{{ID: 50, Name: "api", Status: models.AppStatusRunning, ServerID: 1}},
		func(string, uint) (bool, error) { return true, nil },
	)
	r := &fakeRedeployer{}
	svc.SetRedeployer(r, fakeAppFinder{50: {ID: 50, Name: "api", ServerID: 1}})
	return svc, r
}

// The point of finding C: a missing row was reported and could not be acted on.
func TestApplyRedeploysAConfirmedMissingApp(t *testing.T) {
	svc, r := missingAppService(t, nil)

	res, err := svc.Apply(context.Background(), 1, Selection{
		Missing: []ResourceRef{{Kind: "container", Ref: "app:50"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Redeployed) != 1 || res.Redeployed[0].OwnerID != 50 {
		t.Fatalf("redeployed = %+v; want app 50", res.Redeployed)
	}
	if len(r.calls) != 1 || r.calls[0].appID != 50 || r.calls[0].reason == "" {
		t.Fatalf("calls = %+v; want one redeploy of app 50 with a reason", r.calls)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("errors = %v; want none", res.Errors)
	}
}

// Re-confirmed against fresh state, exactly like orphan removal: an app running again by the time Apply lands
// is left alone rather than deployed a second time.
func TestApplySkipsAnAppThatIsRunningAgain(t *testing.T) {
	live := []docker.Container{{ID: "c50", Names: []string{"/api"}, Labels: map[string]string{labelApp: "50"}, State: "running"}}
	svc, r := missingAppService(t, live)

	res, err := svc.Apply(context.Background(), 1, Selection{
		Missing: []ResourceRef{{Kind: "container", Ref: "app:50"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Redeployed) != 0 || len(r.calls) != 0 {
		t.Fatalf("redeployed=%+v calls=%+v; want nothing done", res.Redeployed, r.calls)
	}
}

// A deploy the service refuses — an app whose data volume is gone is refused by the deploy path itself — is
// reported rather than swallowed.
func TestApplyReportsARefusedRedeploy(t *testing.T) {
	svc, r := missingAppService(t, nil)
	r.err = errors.New("a volume holding this workload's data is gone")

	res, err := svc.Apply(context.Background(), 1, Selection{
		Missing: []ResourceRef{{Kind: "container", Ref: "app:50"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Redeployed) != 0 || len(res.Errors) != 1 {
		t.Fatalf("redeployed=%+v errors=%v; want the refusal reported", res.Redeployed, res.Errors)
	}
}

// Without a redeployer wired, the missing rows stay report-only and say so.
func TestApplyWithoutARedeployerSaysSo(t *testing.T) {
	svc := newTestService(
		&fakeDocker{},
		[]models.Application{{ID: 50, Name: "api", Status: models.AppStatusRunning, ServerID: 1}},
		func(string, uint) (bool, error) { return true, nil },
	)

	res, err := svc.Apply(context.Background(), 1, Selection{
		Missing: []ResourceRef{{Kind: "container", Ref: "app:50"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Redeployed) != 0 || len(res.Errors) != 1 {
		t.Fatalf("redeployed=%+v errors=%v; want one explanatory error", res.Redeployed, res.Errors)
	}
}
