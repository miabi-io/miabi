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

package versioning

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// policyTable mirrors version_policies without the Postgres-only gen_random_uuid() default.
type policyTable struct {
	ID              uint `gorm:"primaryKey"`
	UID             string
	WorkspaceID     uint
	ApplicationID   *uint `gorm:"uniqueIndex"`
	PipelineID      *uint `gorm:"uniqueIndex"`
	Scheme          string
	Template        string
	Series          string
	Bump            string
	Start           string
	CalverFormat    string
	ReleaseBranches string
	OtherBranches   string
	BranchSeries    string
	FloatingTags    string
	BumpFromCommits bool
	ManagedBy       string
	Current         string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (policyTable) TableName() string { return "version_policies" }

func newTestService(t *testing.T) (*Service, *repositories.PipelineRepository, *fakeRegistry) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), dbSeq.Add(1))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&policyTable{}, &models.VersionAllocation{}, &models.PipelineRun{}, &models.PipelineStepRun{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	runs := repositories.NewPipelineRepository(db)
	svc := NewService(repositories.NewVersionRepository(db), nil, runs)
	reg := &fakeRegistry{tags: map[string]string{}}
	svc.SetRegistry(reg)
	return svc, runs, reg
}

type fakeRegistry struct {
	mu   sync.Mutex
	tags map[string]string
}

func (f *fakeRegistry) TagImage(_ context.Context, repository, digest, tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags[repository+":"+tag] = digest
	return nil
}

func (f *fakeRegistry) ImageTagDigest(_ context.Context, repository, tag string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.tags[repository+":"+tag]
	return d, ok, nil
}

const repo = "registry.example.com/ws_1/api"

var dbSeq atomic.Int64

func newRun(t *testing.T, runs *repositories.PipelineRepository, n int) *models.PipelineRun {
	t.Helper()
	run := &models.PipelineRun{WorkspaceID: 1, PipelineID: 4, Number: n, Status: models.PipelineRunRunning, Branch: "main"}
	if err := runs.CreateRun(run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	return run
}

func appPipeline() *models.PipelineDefinition {
	app := uint(9)
	return &models.PipelineDefinition{ID: 4, WorkspaceID: 1, Name: "api", ApplicationID: &app}
}

func finish(svc *Service, run *models.PipelineRun, status models.PipelineRunStatus, digest string) {
	run.Status = status
	svc.Settle(context.Background(), run, digest, repo, func(string) {})
}

func TestReserveSettleLifecycle(t *testing.T) {
	svc, runs, reg := newTestService(t)
	def := appPipeline()
	if _, err := svc.EnsureForPipeline(def, &Settings{FloatingTags: []string{"latest", "v{major}"}}, false); err != nil {
		t.Fatal(err)
	}

	first := newRun(t, runs, 1)
	if err := svc.Reserve(first, def, Request{}); err != nil || first.ImageTag != "v1.0.0" {
		t.Fatalf("first reserve = %q, %v", first.ImageTag, err)
	}
	finish(svc, first, models.PipelineRunSucceeded, "sha256:aaa")
	if reg.tags[repo+":v1.0.0"] != "sha256:aaa" || reg.tags[repo+":latest"] != "sha256:aaa" || reg.tags[repo+":v1"] != "sha256:aaa" {
		t.Fatalf("published tags = %v", reg.tags)
	}

	// A failed run that still holds the head gives its number back.
	failed := newRun(t, runs, 2)
	_ = svc.Reserve(failed, def, Request{})
	finish(svc, failed, models.PipelineRunFailed, "")
	retry := newRun(t, runs, 3)
	_ = svc.Reserve(retry, def, Request{})
	if retry.ImageTag != "v1.0.1" {
		t.Fatalf("retry after a failure = %q, want v1.0.1 reused", retry.ImageTag)
	}

	// A failure behind a newer reservation is skipped, never reused.
	behind := newRun(t, runs, 4)
	_ = svc.Reserve(behind, def, Request{})
	finish(svc, retry, models.PipelineRunFailed, "")
	if _, err := svc.repo.FindAllocationByRun(retry.ID); err != nil {
		t.Fatalf("v1.0.1 should stay as abandoned behind v1.0.2: %v", err)
	}
	finish(svc, behind, models.PipelineRunSucceeded, "sha256:bbb")
	next := newRun(t, runs, 5)
	_ = svc.Reserve(next, def, Request{})
	if next.ImageTag != "v1.0.3" {
		t.Fatalf("next = %q, want v1.0.3", next.ImageTag)
	}

	p, _ := svc.ForPipeline(def)
	if p.Current != "1.0.2" {
		t.Fatalf("current = %q, want 1.0.2", p.Current)
	}
}

func TestFloatingTagsNeverMoveBackwards(t *testing.T) {
	svc, runs, reg := newTestService(t)
	def := appPipeline()
	p, _ := svc.EnsureForPipeline(def, &Settings{Series: "2.x.x", FloatingTags: []string{"latest", "v{major}"}}, false)

	two := newRun(t, runs, 1)
	_ = svc.Reserve(two, def, Request{})
	finish(svc, two, models.PipelineRunSucceeded, "sha256:two")

	if _, err := svc.SwitchSeries(1, p.UID, "1.x.x"); err != nil {
		t.Fatal(err)
	}
	hotfix := newRun(t, runs, 2)
	_ = svc.Reserve(hotfix, def, Request{})
	if hotfix.ImageTag != "v1.0.0" {
		t.Fatalf("hotfix line = %q", hotfix.ImageTag)
	}
	finish(svc, hotfix, models.PipelineRunSucceeded, "sha256:one")
	if reg.tags[repo+":latest"] != "sha256:two" {
		t.Error("a 1.x hotfix moved latest away from 2.0.0")
	}
	if reg.tags[repo+":v1"] != "sha256:one" {
		t.Error("v1 should point at the hotfix")
	}
}

func TestVerifyTagFreeSkipsTakenVersions(t *testing.T) {
	svc, runs, reg := newTestService(t)
	def := appPipeline()
	_, _ = svc.EnsureForPipeline(def, nil, false)
	reg.tags[repo+":v1.0.0"] = "sha256:old"
	reg.tags[repo+":v1.0.1"] = "sha256:old2"

	run := newRun(t, runs, 1)
	_ = svc.Reserve(run, def, Request{})
	if err := svc.VerifyTagFree(context.Background(), run, repo, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if run.ImageTag != "v1.0.2" {
		t.Fatalf("run moved to %q, want v1.0.2", run.ImageTag)
	}

	exact := newRun(t, runs, 2)
	reg.tags[repo+":v1.5.0"] = "sha256:x"
	if err := svc.Reserve(exact, def, Request{Exact: "1.5.0"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.VerifyTagFree(context.Background(), exact, repo, func(string) {}); !errors.Is(err, ErrTagExists) {
		t.Fatalf("a hand-set version must not move silently: %v", err)
	}
}

// newPostgresService runs on a real database, since the row lock that serializes reserves is
// Postgres behaviour; SQLite rejects concurrent writers outright.
func newPostgresService(t *testing.T) (*Service, *repositories.PipelineRepository) {
	t.Helper()
	dsn := os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MIABI_TEST_POSTGRES_DSN to run the concurrent reserve test")
	}
	schema := fmt.Sprintf("t_versioning_%d", time.Now().UnixNano())
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP SCHEMA " + schema + " CASCADE") })
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.VersionPolicy{}, &models.VersionAllocation{}, &models.PipelineRun{}); err != nil {
		t.Fatal(err)
	}
	runs := repositories.NewPipelineRepository(db)
	return NewService(repositories.NewVersionRepository(db), nil, runs), runs
}

func TestConcurrentReservesNeverCollide(t *testing.T) {
	svc, runs := newPostgresService(t)
	def := appPipeline()
	_, _ = svc.EnsureForPipeline(def, nil, false)

	const n = 24
	got := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		run := newRun(t, runs, i+1)
		wg.Add(1)
		go func(i int, run *models.PipelineRun) {
			defer wg.Done()
			if err := svc.Reserve(run, def, Request{}); err != nil {
				t.Errorf("reserve %d: %v", i, err)
			}
			got[i] = run.ImageTag
		}(i, run)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, tag := range got {
		if tag == "" || seen[tag] {
			t.Fatalf("duplicate or missing version in %v", got)
		}
		seen[tag] = true
	}
}

func TestManagedPolicyRefusesAPIEdits(t *testing.T) {
	svc, _, _ := newTestService(t)
	def := appPipeline()
	p, err := svc.EnsureForPipeline(def, &Settings{Series: "3.x.x"}, true)
	if err != nil || p.ManagedBy != "api" || p.Series != "3.x.x" {
		t.Fatalf("managed policy = %+v, %v", p, err)
	}
	if _, err := svc.Update(1, p.UID, DefaultSettings()); !errors.Is(err, ErrManaged) {
		t.Fatalf("update of a managed policy: %v", err)
	}
	svc.Unmanage(def)
	if _, err := svc.Update(1, p.UID, DefaultSettings()); err != nil {
		t.Fatalf("update after the block was dropped: %v", err)
	}
}

func TestNoPolicyLeavesRunTags(t *testing.T) {
	svc, runs, _ := newTestService(t)
	run := newRun(t, runs, 1)
	if err := svc.Reserve(run, appPipeline(), Request{}); err != nil || run.ImageTag != "" {
		t.Fatalf("no policy: tag %q, err %v", run.ImageTag, err)
	}
	if err := svc.Reserve(run, appPipeline(), Request{Bump: models.BumpMinor}); !errors.Is(err, ErrOverrideNotSupported) {
		t.Fatalf("override without a policy: %v", err)
	}
}

func TestSnapshotRoundTripContinuesNumbering(t *testing.T) {
	src, runs, _ := newTestService(t)
	def := appPipeline()
	_, _ = src.EnsureForPipeline(def, &Settings{Series: "2.x.x", Bump: models.BumpMinor}, false)
	for i := 1; i <= 3; i++ {
		run := newRun(t, runs, i)
		_ = src.Reserve(run, def, Request{})
		finish(src, run, models.PipelineRunSucceeded, fmt.Sprintf("sha256:%d", i))
	}
	raw, err := src.Export(def)
	if err != nil || len(raw) == 0 {
		t.Fatalf("export = %s, %v", raw, err)
	}

	dst, dstRuns, _ := newTestService(t)
	restored := appPipeline()
	restored.ID, restored.ApplicationID = 40, ptr(90)
	if _, err := dst.EnsureForPipeline(restored, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := dst.Import(restored, raw); err != nil {
		t.Fatalf("import: %v", err)
	}
	run := newRun(t, dstRuns, 1)
	if err := dst.Reserve(run, restored, Request{}); err != nil || run.ImageTag != "v2.3.0" {
		t.Fatalf("after restore = %q, %v; want v2.3.0", run.ImageTag, err)
	}
}

func ptr(v uint) *uint { return &v }

func TestFloatingTagLeavesForeignTagAlone(t *testing.T) {
	svc, runs, reg := newTestService(t)
	def := appPipeline()
	_, _ = svc.EnsureForPipeline(def, &Settings{FloatingTags: []string{"v{major}"}}, false)
	reg.tags[repo+":v1"] = "sha256:legacy-release-1"

	run := newRun(t, runs, 1)
	_ = svc.Reserve(run, def, Request{})
	finish(svc, run, models.PipelineRunSucceeded, "sha256:new")
	if reg.tags[repo+":v1"] != "sha256:legacy-release-1" {
		t.Fatal("a pre-existing v1 label was overwritten by the v{major} floating tag")
	}

	reg.tags[repo+":v2"] = "sha256:new"
	next := newRun(t, runs, 2)
	_ = svc.Reserve(next, def, Request{})
	finish(svc, next, models.PipelineRunSucceeded, "sha256:newer")
	if reg.tags[repo+":v1"] != "sha256:legacy-release-1" {
		t.Fatal("still foreign: must not move")
	}
}
