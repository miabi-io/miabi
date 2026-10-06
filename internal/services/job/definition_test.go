// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
			name TEXT, command TEXT, entrypoint TEXT, image TEXT, registry_id INTEGER, run_as_user TEXT,
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

// A run applied while the app deploys waits for that deploy, even for a git app
// that has no image to run yet.
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

// onRelease runs once per release: on create against the active release, then
// once for each new release, however many times the activation is reported.
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

// A failed run retries until backoffLimit is spent; a run superseded by a newer
// one doesn't retry.
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
