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

package job

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeEnqueuer struct{ ids []uint }

func (f *fakeEnqueuer) EnqueueRunJob(jobID, _ uint) error {
	f.ids = append(f.ids, jobID)
	return nil
}

func newDefinitionSvc(t *testing.T) (*Service, *gorm.DB, *fakeEnqueuer) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// By hand where the models carry Postgres-only defaults.
	for _, ddl := range []string{
		`CREATE TABLE applications (id INTEGER PRIMARY KEY, workspace_id INTEGER, name TEXT, server_id INTEGER,
			source_type TEXT, image TEXT, tag TEXT, run_as_user TEXT, stack_id INTEGER)`,
		`CREATE TABLE app_env_vars (id INTEGER PRIMARY KEY, application_id INTEGER, key TEXT, value TEXT)`,
		`CREATE TABLE app_ports (id INTEGER PRIMARY KEY, application_id INTEGER)`,
		`CREATE TABLE networks (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE application_networks (application_id INTEGER, network_id INTEGER)`,
		`CREATE TABLE releases (id INTEGER PRIMARY KEY, application_id INTEGER, image TEXT, active BOOLEAN)`,
		`CREATE TABLE deployments (id INTEGER PRIMARY KEY, application_id INTEGER, status TEXT, number INTEGER)`,
		`CREATE TABLE job_definitions (id INTEGER PRIMARY KEY, uid TEXT, workspace_id INTEGER, application_id INTEGER,
			name TEXT, command TEXT, entrypoint TEXT, image TEXT, registry_id INTEGER, run_as_user TEXT, env TEXT, inherit_env BOOLEAN DEFAULT true, pinned_server_id INTEGER DEFAULT 0,
			timeout_secs INTEGER, run_policy TEXT, wait_for_deploy BOOLEAN, history_limit INTEGER, spec_hash TEXT,
			last_release_id INTEGER, last_job_id INTEGER, backoff_limit INTEGER, metadata TEXT, annotations TEXT,
			created_at DATETIME, updated_at DATETIME)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AutoMigrate(&models.Job{}); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO applications (id, workspace_id, name, source_type, image, tag) VALUES
		(1, 1, 'api', 'image', 'ghcr.io/org/api', 'v1'), (2, 1, 'web', 'git', '', '')`)
	enq := &fakeEnqueuer{}
	s := NewService(repositories.NewJobRepository(db), repositories.NewApplicationRepository(db),
		repositories.NewReleaseRepository(db), nil, enq, 600)
	s.SetQuota(&quota.Service{})
	s.SetDeployments(repositories.NewDeploymentRepository(db))
	return s, db, enq
}

func definitionInput(app uint, policy string) DefinitionInput {
	return DefinitionInput{Name: "migrate", ApplicationID: app, Command: []string{"./api", "migrate"},
		RunPolicy: policy, WaitForDeploy: policy == models.JobRunOnChange, SpecHash: "h1"}
}

func TestSaveDefinitionRuns(t *testing.T) {
	s, _, enq := newDefinitionSvc(t)
	ctx := context.Background()
	def, j, err := s.SaveDefinition(ctx, 1, definitionInput(1, models.JobRunOnChange), true)
	if err != nil || j == nil {
		t.Fatalf("create: run = %v, err = %v", j, err)
	}
	if j.JobDefinitionID == nil || *j.JobDefinitionID != def.ID || j.Source != models.JobSourceGitOps || j.Image != "ghcr.io/org/api:v1" {
		t.Errorf("run = %+v", j)
	}
	if _, j, err = s.SaveDefinition(ctx, 1, definitionInput(1, models.JobRunOnChange), false); err != nil || j != nil {
		t.Errorf("update without run: run = %v, err = %v", j, err)
	}
	if len(enq.ids) != 1 {
		t.Errorf("enqueued %d runs, want 1", len(enq.ids))
	}
	if _, _, err := s.SaveDefinition(ctx, 1, definitionInput(2, models.JobRunOnChange), false); !errors.Is(err, ErrDefinitionAppMoved) {
		t.Errorf("moving app: err = %v", err)
	}
}

func TestSaveDefinitionWaitsForDeploy(t *testing.T) {
	s, db, _ := newDefinitionSvc(t)
	db.Exec(`INSERT INTO deployments (id, application_id, status, number) VALUES (9, 2, 'building', 3)`)
	_, j, err := s.SaveDefinition(context.Background(), 1, definitionInput(2, models.JobRunOnChange), true)
	if err != nil {
		t.Fatal(err)
	}
	if j.WaitDeploymentID == nil || *j.WaitDeploymentID != 9 {
		t.Errorf("wait deployment = %v, want 9", j.WaitDeploymentID)
	}
}

func TestOnReleaseRunsOncePerRelease(t *testing.T) {
	s, db, enq := newDefinitionSvc(t)
	db.Exec(`INSERT INTO releases (id, application_id, image, active) VALUES (5, 1, 'ghcr.io/org/api:v1', 1)`)
	ctx := context.Background()
	def, j, err := s.SaveDefinition(ctx, 1, definitionInput(1, models.JobRunOnRelease), true)
	if err != nil || j == nil || j.Source != models.JobSourceRelease {
		t.Fatalf("create: run = %+v, err = %v", j, err)
	}
	s.ReleaseActivated(1, 5)
	if len(enq.ids) != 1 {
		t.Fatalf("the release it ran on should not run again, enqueued %d", len(enq.ids))
	}
	s.ReleaseActivated(1, 6)
	s.ReleaseActivated(1, 6)
	if len(enq.ids) != 2 {
		t.Errorf("a new release should run once, enqueued %d", len(enq.ids))
	}
	if _, j, _ := s.SaveDefinition(ctx, 1, definitionInput(1, models.JobRunOnRelease), true); j != nil {
		t.Error("an onRelease update should not run")
	}
	got, _ := s.repo.FindJobDefinitionInWorkspace(1, def.ID)
	if got.LastReleaseID == nil || *got.LastReleaseID != 6 {
		t.Errorf("last release = %v, want 6", got.LastReleaseID)
	}
}

// A new onRelease Job whose app is deploying leaves the run to that deploy's activation.
func TestOnReleaseCreateDuringDeploy(t *testing.T) {
	s, db, enq := newDefinitionSvc(t)
	db.Exec(`INSERT INTO releases (id, application_id, image, active) VALUES (5, 1, 'img', 1)`)
	db.Exec(`INSERT INTO deployments (id, application_id, status, number) VALUES (9, 1, 'deploying', 3)`)
	if _, j, err := s.SaveDefinition(context.Background(), 1, definitionInput(1, models.JobRunOnRelease), true); err != nil || j != nil {
		t.Fatalf("run = %v, err = %v", j, err)
	}
	s.ReleaseActivated(1, 7)
	if len(enq.ids) != 1 {
		t.Errorf("the deploy's release should run it once, enqueued %d", len(enq.ids))
	}
}

func TestDeleteDefinitionKeepsHistory(t *testing.T) {
	s, db, _ := newDefinitionSvc(t)
	ctx := context.Background()
	def, j, err := s.SaveDefinition(ctx, 1, definitionInput(1, models.JobRunOnChange), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDefinition(ctx, 1, def.ID); err != nil {
		t.Fatal(err)
	}
	var run models.Job
	db.First(&run, j.ID)
	if run.Status != models.JobCanceled || run.JobDefinitionID != nil {
		t.Errorf("run after delete = status %s, definition %v", run.Status, run.JobDefinitionID)
	}
	if _, err := s.GetDefinitionByName(1, "migrate"); !errors.Is(err, ErrDefinitionNotFound) {
		t.Errorf("definition should be gone, err = %v", err)
	}
}

func TestRunFailedRetries(t *testing.T) {
	s, db, enq := newDefinitionSvc(t)
	in := definitionInput(1, models.JobRunOnChange)
	in.BackoffLimit = 1
	_, j, err := s.SaveDefinition(context.Background(), 1, in, true)
	if err != nil {
		t.Fatal(err)
	}
	db.Model(&models.Job{}).Where("id = ?", j.ID).Update("status", models.JobFailed)
	s.RunFailed(j.ID)
	if len(enq.ids) != 2 {
		t.Fatalf("first failure should retry, enqueued %d", len(enq.ids))
	}
	var retry models.Job
	db.Last(&retry)
	if retry.Attempt != 2 {
		t.Errorf("retry attempt = %d, want 2", retry.Attempt)
	}
	db.Model(&models.Job{}).Where("id = ?", retry.ID).Update("status", models.JobFailed)
	s.RunFailed(retry.ID)
	s.RunFailed(j.ID)
	if len(enq.ids) != 2 {
		t.Errorf("an exhausted or superseded run should not retry, enqueued %d", len(enq.ids))
	}
}

func TestBeforeReleaseNeverRunsOnSave(t *testing.T) {
	s, db, enq := newDefinitionSvc(t)
	db.Exec(`INSERT INTO releases (id, application_id, image, active) VALUES (5, 1, 'ghcr.io/org/api:v1', 1)`)
	in := definitionInput(1, models.JobRunBeforeRelease)
	in.TimeoutSecs = 60
	def, j, err := s.SaveDefinition(context.Background(), 1, in, true)
	if err != nil || j != nil || len(enq.ids) != 0 {
		t.Fatalf("save ran something: run = %v, enqueued = %v, err = %v", j, enq.ids, err)
	}
	if def.RunPolicy != models.JobRunBeforeRelease {
		t.Errorf("policy = %q", def.RunPolicy)
	}
}

func TestBeforeReleaseRunIsNotQueued(t *testing.T) {
	s, _, enq := newDefinitionSvc(t)
	in := definitionInput(2, models.JobRunBeforeRelease)
	in.TimeoutSecs = 60
	def, _, err := s.SaveDefinition(context.Background(), 1, in, true)
	if err != nil {
		t.Fatal(err)
	}
	// A Git app with no release yet: the run takes the deploy's freshly built image.
	j, err := s.NewBeforeReleaseRun(def, 9, "registry.local/ws_1/web:9", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(enq.ids) != 0 || j.Status != models.JobPending || j.Pull || j.Image != "registry.local/ws_1/web:9" ||
		j.Source != models.JobSourceBeforeRelease || j.DeploymentID == nil || *j.DeploymentID != 9 || j.TimeoutSecs != 60 {
		t.Fatalf("run = %+v, enqueued = %v", j, enq.ids)
	}
	if s.SucceededForDeployment(def.ID, 9) {
		t.Error("a pending run is not a success")
	}
	// A retried deploy ends the run the worker restart cut off.
	j.Status = models.JobRunning
	if err := s.repo.Update(j); err != nil {
		t.Fatal(err)
	}
	retry, err := s.NewBeforeReleaseRun(def, 9, "registry.local/ws_1/web:9", 1)
	if err != nil {
		t.Fatal(err)
	}
	if old, _ := s.repo.FindByID(j.ID); old.Status != models.JobCanceled || old.Error == "" {
		t.Errorf("interrupted run = %s %q, want canceled with a reason", old.Status, old.Error)
	}
	j = retry
	j.Status = models.JobSucceeded
	if err := s.repo.Update(j); err != nil {
		t.Fatal(err)
	}
	if !s.SucceededForDeployment(def.ID, 9) || s.SucceededForDeployment(def.ID, 10) {
		t.Error("success should count for its own deployment only")
	}
	defs, _ := s.BeforeReleaseDefinitions(2)
	if len(defs) != 1 || defs[0].ID != def.ID {
		t.Errorf("definitions = %+v", defs)
	}
}

func TestPreReleaseCommand(t *testing.T) {
	s, _, enq := newDefinitionSvc(t)
	ctx := context.Background()
	app, err := s.App(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if def, err := s.GetPreReleaseCommand(1, app); err != nil || def != nil {
		t.Fatalf("unset: def = %v, err = %v", def, err)
	}
	for name, in := range map[string]PreReleaseCommand{
		"no timeout":  {Command: []string{"x"}},
		"over budget": {Command: []string{"x"}, TimeoutSecs: 1000, BackoffLimit: 1},
		"no command":  {TimeoutSecs: 60},
	} {
		if _, err := s.SetPreReleaseCommand(ctx, 1, app, in); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}
	def, err := s.SetPreReleaseCommand(ctx, 1, app, PreReleaseCommand{Command: []string{"./api", "migrate"}, TimeoutSecs: 300, BackoffLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "api-pre-release" || def.RunPolicy != models.JobRunBeforeRelease || len(enq.ids) != 0 {
		t.Fatalf("def = %+v, enqueued = %v", def, enq.ids)
	}
	got, err := s.GetPreReleaseCommand(1, app)
	if err != nil || got == nil || got.TimeoutSecs != 300 || got.BackoffLimit != 1 {
		t.Fatalf("get = %+v, err = %v", got, err)
	}
	if err := s.DeletePreReleaseCommand(ctx, 1, app); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetPreReleaseCommand(1, app); got != nil {
		t.Error("the command should be gone")
	}
}

// A declared Job that happens to use the name isn't the console's to overwrite.
func TestPreReleaseCommandRefusesForeignDefinition(t *testing.T) {
	s, _, _ := newDefinitionSvc(t)
	ctx := context.Background()
	in := definitionInput(1, models.JobRunOnChange)
	in.Name = "api-pre-release"
	if _, _, err := s.SaveDefinition(ctx, 1, in, false); err != nil {
		t.Fatal(err)
	}
	app, _ := s.App(1, 1)
	_, err := s.SetPreReleaseCommand(ctx, 1, app, PreReleaseCommand{Command: []string{"x"}, TimeoutSecs: 60})
	if !errors.Is(err, ErrPreReleaseNameTaken) {
		t.Errorf("err = %v, want name taken", err)
	}
}

func TestPreReleaseSummariesAndFilters(t *testing.T) {
	s, db, _ := newDefinitionSvc(t)
	dep := func(id uint) *uint { return &id }
	for _, j := range []models.Job{
		{WorkspaceID: 1, ApplicationID: 1, Name: "migrate", Status: models.JobFailed, Source: models.JobSourceBeforeRelease, DeploymentID: dep(5)},
		{WorkspaceID: 1, ApplicationID: 1, Name: "migrate", Status: models.JobSucceeded, Source: models.JobSourceBeforeRelease, DeploymentID: dep(5)},
		{WorkspaceID: 1, ApplicationID: 1, Name: "migrate", Status: models.JobFailed, Source: models.JobSourceBeforeRelease, DeploymentID: dep(6)},
		{WorkspaceID: 1, ApplicationID: 2, Name: "fix", Status: models.JobFailed, Source: models.JobSourceManual},
	} {
		if err := db.Create(&j).Error; err != nil {
			t.Fatal(err)
		}
	}
	sums, err := repositories.NewDeploymentRepository(db).PreReleaseSummaries(1, []uint{5, 6, 7})
	if err != nil {
		t.Fatal(err)
	}
	if got := sums[5]; got.Runs != 2 || got.Status != models.JobSucceeded {
		t.Errorf("deployment 5 = %+v, want 2 runs ending succeeded", got)
	}
	if got := sums[6]; got.Runs != 1 || got.Status != models.JobFailed {
		t.Errorf("deployment 6 = %+v, want 1 failed run", got)
	}
	if _, ok := sums[7]; ok {
		t.Error("a deployment without runs has no summary")
	}
	failed, _ := s.List(1, repositories.JobFilter{Status: models.JobFailed}, 0)
	pre, _ := s.List(1, repositories.JobFilter{Status: models.JobFailed, Source: models.JobSourceBeforeRelease}, 0)
	app2, _ := s.List(1, repositories.JobFilter{AppID: 2}, 0)
	if len(failed) != 3 || len(pre) != 2 || len(app2) != 1 {
		t.Errorf("failed = %d, failed pre-release = %d, app 2 = %d; want 3, 2, 1", len(failed), len(pre), len(app2))
	}
}
