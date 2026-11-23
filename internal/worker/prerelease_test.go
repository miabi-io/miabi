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
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

const preImage = "registry.example/web@sha256:abc"

type fakePreDefs struct {
	defs      []models.JobDefinition
	succeeded map[uint]bool
	runs      []*models.Job
}

func (f *fakePreDefs) BeforeReleaseDefinitions(uint) ([]models.JobDefinition, error) {
	return f.defs, nil
}

func (f *fakePreDefs) SucceededForDeployment(defID, _ uint) bool { return f.succeeded[defID] }

func (f *fakePreDefs) NewBeforeReleaseRun(def *models.JobDefinition, deploymentID uint, image string, attempt int) (*models.Job, error) {
	j := &models.Job{ID: uint(len(f.runs) + 1), Name: def.Name, Image: image, Attempt: attempt, DeploymentID: &deploymentID}
	f.runs = append(f.runs, j)
	return j, nil
}

// fakePreRunner ends each run with the next outcome, and records the release it was given.
type fakePreRunner struct {
	outcomes []models.JobStatus
	releases []managedRun
	jobs     map[uint]*models.Job
}

func (f *fakePreRunner) runBeforeRelease(_ context.Context, j *models.Job, release managedRun) {
	f.releases = append(f.releases, release)
	status := models.JobSucceeded
	if len(f.outcomes) > 0 {
		status, f.outcomes = f.outcomes[0], f.outcomes[1:]
	}
	done := *j
	done.Status = status
	done.Logs = "migrating\n"
	if status != models.JobSucceeded {
		done.Error = "command exited with code 1"
	}
	if f.jobs == nil {
		f.jobs = map[uint]*models.Job{}
	}
	f.jobs[j.ID] = &done
}

func (f *fakePreRunner) result(id uint) (*models.Job, error) { return f.jobs[id], nil }

func newPreReleaseHarness(t *testing.T, defs ...models.JobDefinition) (*DeployHandler, *fakePreDefs, *fakePreRunner, *models.Application, *models.Deployment) {
	t.Helper()
	h, _, db := newSwapHarness(t)
	app := &models.Application{Name: "web", WorkspaceID: 1, Status: models.AppStatusRunning}
	if err := db.Create(app).Error; err != nil {
		t.Fatal(err)
	}
	dep := &models.Deployment{ApplicationID: app.ID, Status: models.DeploymentBuilding, Trigger: "manual", Image: preImage}
	if err := db.Create(dep).Error; err != nil {
		t.Fatal(err)
	}
	src := &fakePreDefs{defs: defs, succeeded: map[uint]bool{}}
	runner := &fakePreRunner{}
	h.preDefs, h.preJobs = src, runner
	return h, src, runner, app, dep
}

func migrateDef(id uint, name string, backoff int) models.JobDefinition {
	return models.JobDefinition{ID: id, Name: name, BackoffLimit: backoff, RunPolicy: models.JobRunBeforeRelease}
}

func depState(t *testing.T, h *DeployHandler, id uint) *models.Deployment {
	t.Helper()
	d, err := h.deployments.FindByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPreReleaseSuccessContinuesDeploy(t *testing.T) {
	h, src, runner, app, dep := newPreReleaseHarness(t, migrateDef(1, "migrate", 0))
	if !h.runPreRelease(context.Background(), app, dep, preImage) {
		t.Fatal("a passing job should let the deploy continue")
	}
	if len(src.runs) != 1 || src.runs[0].Image != preImage || *src.runs[0].DeploymentID != dep.ID {
		t.Fatalf("runs = %+v, want one run in the deploy's image linked to it", src.runs)
	}
	if got := runner.releases[0]; got.deploymentID != dep.ID || got.release != 1 || got.digest != "sha256:abc" {
		t.Errorf("release env = %+v, want the upcoming release v1 of this deployment", got)
	}
	logs := depState(t, h, dep.ID).Logs
	if !strings.Contains(logs, "[migrate] migrating") || !strings.Contains(logs, `job "migrate" succeeded`) {
		t.Errorf("deployment log lacks the job output: %q", logs)
	}
}

func TestPreReleaseFailureFailsDeploy(t *testing.T) {
	h, src, runner, app, dep := newPreReleaseHarness(t, migrateDef(1, "migrate", 0), migrateDef(2, "seed", 0))
	runner.outcomes = []models.JobStatus{models.JobFailed}
	if h.runPreRelease(context.Background(), app, dep, preImage) {
		t.Fatal("a failing job should stop the deploy")
	}
	got := depState(t, h, dep.ID)
	if got.Status != models.DeploymentFailed || !strings.Contains(got.Error, `pre-release job "migrate" failed`) {
		t.Errorf("deployment = %s %q, want failed by the pre-release job", got.Status, got.Error)
	}
	if len(src.runs) != 1 {
		t.Errorf("ran %d jobs, want the second one never started", len(src.runs))
	}
}

// The job runs before a recreate stops anything, so the app is still running on its current release.
func TestPreReleaseFailureKeepsRecreateAppRunning(t *testing.T) {
	h, _, runner, app, dep := newPreReleaseHarness(t, migrateDef(1, "migrate", 0))
	if err := h.apps.SetCurrentRelease(app.ID, 42, models.AppStatusRunning); err != nil {
		t.Fatal(err)
	}
	dep.Strategy = models.DeployRecreate
	runner.outcomes = []models.JobStatus{models.JobFailed}
	if h.runPreRelease(context.Background(), app, dep, preImage) {
		t.Fatal("a failing job should stop the deploy")
	}
	got, err := h.apps.FindByID(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.AppStatusRunning {
		t.Errorf("app status = %s, want running", got.Status)
	}
}

func TestPreReleaseRetriesWithinBackoff(t *testing.T) {
	h, src, runner, app, dep := newPreReleaseHarness(t, migrateDef(1, "migrate", 2))
	runner.outcomes = []models.JobStatus{models.JobFailed, models.JobSucceeded}
	if !h.runPreRelease(context.Background(), app, dep, preImage) {
		t.Fatal("the retry passed, so the deploy should continue")
	}
	if len(src.runs) != 2 || src.runs[1].Attempt != 2 {
		t.Errorf("runs = %d, want a second attempt", len(src.runs))
	}
}

func TestPreReleaseBackoffExhausted(t *testing.T) {
	h, src, runner, app, dep := newPreReleaseHarness(t, migrateDef(1, "migrate", 1))
	runner.outcomes = []models.JobStatus{models.JobFailed, models.JobFailed}
	if h.runPreRelease(context.Background(), app, dep, preImage) {
		t.Fatal("every attempt failed, so the deploy should fail")
	}
	if len(src.runs) != 2 {
		t.Errorf("runs = %d, want 1 + backoffLimit", len(src.runs))
	}
}

func TestPreReleaseSkippedOnRollbackAndPromote(t *testing.T) {
	for _, trigger := range []string{"rollback", "promote", "start", "reconcile", "migration"} {
		t.Run(trigger, func(t *testing.T) {
			h, src, _, app, dep := newPreReleaseHarness(t, migrateDef(1, "migrate", 0))
			dep.Trigger = trigger
			if !h.runPreRelease(context.Background(), app, dep, preImage) || len(src.runs) != 0 {
				t.Fatalf("trigger %s ran %d jobs, want none", trigger, len(src.runs))
			}
			if !strings.Contains(depState(t, h, dep.ID).Logs, "pre-release: skipped") {
				t.Error("the skip should be logged")
			}
		})
	}
}

func TestPreReleaseRetriedDeploymentSkipsSucceededJob(t *testing.T) {
	h, src, _, app, dep := newPreReleaseHarness(t, migrateDef(1, "migrate", 0), migrateDef(2, "seed", 0))
	src.succeeded[1] = true
	if !h.runPreRelease(context.Background(), app, dep, preImage) {
		t.Fatal("deploy should continue")
	}
	if len(src.runs) != 1 || src.runs[0].Name != "seed" {
		t.Errorf("runs = %+v, want only the job that had not succeeded", src.runs)
	}
}

func TestPreReleaseNoDefinitionsIsNoop(t *testing.T) {
	h, src, _, app, dep := newPreReleaseHarness(t)
	if !h.runPreRelease(context.Background(), app, dep, preImage) || len(src.runs) != 0 {
		t.Fatal("an app without beforeRelease jobs deploys as before")
	}
	if strings.Contains(depState(t, h, dep.ID).Logs, "pre-release") {
		t.Error("nothing should be logged without definitions")
	}
}
