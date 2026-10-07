// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// cronRow is models.CronJob without the Postgres-only uid default sqlite can't migrate.
type cronRow struct {
	ID                uint `gorm:"primaryKey"`
	UID               string
	WorkspaceID       uint
	ApplicationID     uint
	Name              string
	DisplayName       string
	Schedule          string
	Command           string
	Entrypoint        string
	TimeoutSecs       int
	Image             string
	RegistryID        *uint
	RunAsUser         string
	Enabled           bool
	ConcurrencyPolicy string
	HistoryLimit      int
	Metadata          string
	Annotations       string
	LastRunAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (cronRow) TableName() string { return "cron_jobs" }

type fakeScheduler struct{ reg map[uint]string }

func (f *fakeScheduler) RegisterTask(_ string, id uint, _, schedule string, _ func() error) error {
	f.reg[id] = schedule
	return nil
}

func (f *fakeScheduler) UnregisterTask(_ string, id uint) { delete(f.reg, id) }

func newCronSvc(t *testing.T) (*Service, *repositories.JobRepository, *fakeScheduler) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cronRow{}); err != nil {
		t.Fatal(err)
	}
	repo := repositories.NewJobRepository(db)
	sch := &fakeScheduler{reg: map[uint]string{}}
	return &Service{repo: repo, quota: &quota.Service{}, scheduler: sch}, repo, sch
}

func seedCron(t *testing.T, repo *repositories.JobRepository, cj models.CronJob) *models.CronJob {
	t.Helper()
	if err := repo.CreateCronJob(&cj); err != nil {
		t.Fatal(err)
	}
	return &cj
}

// A tick for a CronJob deleted elsewhere (another replica, or with its app)
// unregisters itself instead of failing on every fire.
func TestTickUnregistersDeletedCronJob(t *testing.T) {
	s, repo, sch := newCronSvc(t)
	cj := seedCron(t, repo, models.CronJob{WorkspaceID: 1, ApplicationID: 1, Name: "report", Schedule: "* * * * *", Enabled: true})
	s.schedule(cj)
	if err := repo.DeleteCronJob(cj.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.tick(cj.ID); err != nil {
		t.Fatalf("tick on a deleted cronjob: %v", err)
	}
	if _, ok := sch.reg[cj.ID]; ok {
		t.Error("tick should unregister a deleted cronjob")
	}
}

// The leader picks up schedules created, edited or deleted on other replicas.
func TestSyncSchedules(t *testing.T) {
	s, repo, sch := newCronSvc(t)
	added := seedCron(t, repo, models.CronJob{WorkspaceID: 1, ApplicationID: 1, Name: "added", Schedule: "0 1 * * *", Enabled: true})
	edited := seedCron(t, repo, models.CronJob{WorkspaceID: 1, ApplicationID: 1, Name: "edited", Schedule: "0 2 * * *", Enabled: true})
	s.schedule(edited)
	edited.Schedule = "0 3 * * *"
	if err := repo.UpdateCronJob(edited); err != nil {
		t.Fatal(err)
	}
	gone := seedCron(t, repo, models.CronJob{WorkspaceID: 1, ApplicationID: 1, Name: "gone", Schedule: "0 4 * * *", Enabled: true})
	s.schedule(gone)
	_ = repo.DeleteCronJob(gone.ID)

	if err := s.SyncSchedules(); err != nil {
		t.Fatal(err)
	}
	if sch.reg[added.ID] != "0 1 * * *" || sch.reg[edited.ID] != "0 3 * * *" {
		t.Errorf("registered = %v", sch.reg)
	}
	if _, ok := sch.reg[gone.ID]; ok {
		t.Error("a deleted cronjob should be unregistered")
	}
}

// Recording a run writes only last_run_at, so a tick holding a stale copy can't
// revert an edit made meanwhile.
func TestLastRunDoesNotRevertEdit(t *testing.T) {
	_, repo, _ := newCronSvc(t)
	cj := seedCron(t, repo, models.CronJob{WorkspaceID: 1, ApplicationID: 1, Name: "report", Schedule: "0 1 * * *", Enabled: true})
	stale := *cj
	cj.Schedule = "0 5 * * *"
	if err := repo.UpdateCronJob(cj); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateCronJobLastRun(stale.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.FindCronJobByID(cj.ID)
	if got.Schedule != "0 5 * * *" || got.LastRunAt == nil {
		t.Errorf("schedule = %q, last run = %v", got.Schedule, got.LastRunAt)
	}
}

func TestCronName(t *testing.T) {
	s, repo, _ := newCronSvc(t)
	seedCron(t, repo, models.CronJob{WorkspaceID: 1, ApplicationID: 1, Name: "nightly-report", Schedule: "0 1 * * *"})

	if _, _, err := s.cronName(1, "Nightly Report", "", 0); !errors.Is(err, ErrCronNameTaken) {
		t.Errorf("duplicate slug: err = %v", err)
	}
	if n, d, err := s.cronName(2, "Nightly Report", "", 0); err != nil || n != "nightly-report" || d != "Nightly Report" {
		t.Errorf("other workspace = %q/%q (%v)", n, d, err)
	}
	if n, _, err := s.cronName(1, "nightly-report", "", 1); err != nil || n != "nightly-report" {
		t.Errorf("renaming to its own name = %q (%v)", n, err)
	}
	if n, _, err := s.cronName(1, "", "", 0); err != nil || n != "cronjob" {
		t.Errorf("blank = %q (%v)", n, err)
	}
}

// A GitOps-owned CronJob accepts a pause or resume from the console, and nothing else.
func TestOnlyToggles(t *testing.T) {
	cj := &models.CronJob{Name: "report", Schedule: "0 1 * * *", Command: []string{"x"}, ConcurrencyPolicy: "allow", Enabled: true}
	in := CronJobInput{Name: "report", Schedule: "0 1 * * *", Command: []string{"x"}, Enabled: false}
	if !in.OnlyToggles(cj) {
		t.Error("pausing should be allowed")
	}
	in.Schedule = "0 2 * * *"
	if in.OnlyToggles(cj) {
		t.Error("a schedule change should be refused")
	}
	in.Schedule, in.Command = cj.Schedule, []string{"y"}
	if in.OnlyToggles(cj) {
		t.Error("a command change should be refused")
	}
}
