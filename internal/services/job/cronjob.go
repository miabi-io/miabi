// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"context"
	"errors"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/robfig/cron/v3"
)

const cronKind = "cronjob"

// defaultHistoryLimit caps spawned-job history per cronjob when unset.
const defaultHistoryLimit = 20

var (
	ErrInvalidSchedule = errors.New("invalid cron schedule")
	ErrCronNotFound    = errors.New("cronjob not found")
)

// Scheduler registers/unregisters recurring tasks. Implemented by cron.Manager;
// an interface here keeps the job package free of a hard cron dependency.
type Scheduler interface {
	RegisterTask(kind string, id uint, name, schedule string, fn func() error) error
	UnregisterTask(kind string, id uint)
}

// SetScheduler wires the cron scheduler used to drive CronJobs. Optional: when
// unset (e.g. in the worker process), CronJob CRUD still works but nothing is
// scheduled in this process.
func (s *Service) SetScheduler(sch Scheduler) { s.scheduler = sch }

// CronJobInput is the create/update payload for a CronJob.
type CronJobInput struct {
	Name       string
	Schedule   string
	Command    []string
	Entrypoint []string
	Image      string // optional custom image override (blank = app's release)
	RegistryID *uint
	// RunAsUser pins spawned runs to an account (blank = inherit the app's).
	RunAsUser         string
	TimeoutSecs       int
	Enabled           bool
	ConcurrencyPolicy string
	HistoryLimit      int
}

// LoadCronJobs registers every enabled CronJob with the scheduler. Call once at
// startup, after the scheduler is running.
func (s *Service) LoadCronJobs() {
	if s.scheduler == nil {
		return
	}
	list, err := s.repo.ListEnabledCronJobs()
	if err != nil {
		logger.Error("failed to load cronjobs", "error", err)
		return
	}
	for i := range list {
		s.schedule(&list[i])
	}
	logger.Info("cronjobs loaded", "count", len(list))
}

func (s *Service) CreateCronJob(workspaceID, appID uint, in CronJobInput) (*models.CronJob, error) {
	if len(in.Command) == 0 {
		return nil, ErrNoCommand
	}
	if err := validateCron(in.Schedule); err != nil {
		return nil, ErrInvalidSchedule
	}
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, ErrNotFound
	}
	// Blank stays blank on a schedule: spawned runs inherit whatever the app is
	// configured with when they fire, rather than snapshotting it now.
	runAsUser, err := s.checkRunAsUser(app, in.RunAsUser)
	if err != nil {
		return nil, err
	}
	if s.quota.Enabled() {
		n, _ := s.repo.CountCronByWorkspace(workspaceID)
		if err := s.quota.CheckCreate(workspaceID, quota.ResourceCronJobs, int(n)); err != nil {
			return nil, err
		}
	}
	cj := &models.CronJob{
		WorkspaceID:       workspaceID,
		ApplicationID:     appID,
		Name:              in.Name,
		Schedule:          in.Schedule,
		Command:           in.Command,
		Entrypoint:        in.Entrypoint,
		Image:             in.Image,
		RegistryID:        in.RegistryID,
		RunAsUser:         runAsUser,
		TimeoutSecs:       in.TimeoutSecs,
		Enabled:           in.Enabled,
		ConcurrencyPolicy: normalizePolicy(in.ConcurrencyPolicy),
		HistoryLimit:      in.HistoryLimit,
	}
	if err := s.repo.CreateCronJob(cj); err != nil {
		return nil, err
	}
	if cj.Enabled {
		s.schedule(cj)
	}
	return cj, nil
}

func (s *Service) UpdateCronJob(workspaceID, id uint, in CronJobInput) (*models.CronJob, error) {
	cj, err := s.repo.FindCronJobInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrCronNotFound
	}
	if len(in.Command) == 0 {
		return nil, ErrNoCommand
	}
	if err := validateCron(in.Schedule); err != nil {
		return nil, ErrInvalidSchedule
	}
	cj.Name = in.Name
	cj.Schedule = in.Schedule
	cj.Command = in.Command
	cj.Entrypoint = in.Entrypoint
	app, err := s.apps.FindInWorkspace(workspaceID, cj.ApplicationID)
	if err != nil {
		return nil, ErrNotFound
	}
	// Re-validated on update so a schedule can't outlive a plan that later mandates non-root.
	if cj.RunAsUser, err = s.checkRunAsUser(app, in.RunAsUser); err != nil {
		return nil, err
	}
	cj.Image = in.Image
	cj.RegistryID = in.RegistryID
	cj.TimeoutSecs = in.TimeoutSecs
	cj.Enabled = in.Enabled
	cj.ConcurrencyPolicy = normalizePolicy(in.ConcurrencyPolicy)
	cj.HistoryLimit = in.HistoryLimit
	if err := s.repo.UpdateCronJob(cj); err != nil {
		return nil, err
	}
	// Re-register to pick up schedule/command changes, or unregister if disabled.
	if cj.Enabled {
		s.schedule(cj)
	} else if s.scheduler != nil {
		s.scheduler.UnregisterTask(cronKind, cj.ID)
	}
	return cj, nil
}

// ListCronJobs returns the workspace's cronjobs (optionally filtered to appID),
// annotated with each one's application name.
func (s *Service) ListCronJobs(workspaceID, appID uint) ([]models.CronJob, error) {
	var (
		list []models.CronJob
		err  error
	)
	if appID > 0 {
		list, err = s.repo.ListCronJobsByApp(workspaceID, appID)
	} else {
		list, err = s.repo.ListCronJobsByWorkspace(workspaceID)
	}
	if err != nil {
		return nil, err
	}
	names := s.appNames(workspaceID)
	for i := range list {
		list[i].AppName = names[list[i].ApplicationID]
	}
	return list, nil
}

func (s *Service) GetCronJob(workspaceID, id uint) (*models.CronJob, error) {
	cj, err := s.repo.FindCronJobInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrCronNotFound
	}
	return cj, nil
}

func (s *Service) DeleteCronJob(workspaceID, id uint) error {
	cj, err := s.repo.FindCronJobInWorkspace(workspaceID, id)
	if err != nil {
		return ErrCronNotFound
	}
	if s.scheduler != nil {
		s.scheduler.UnregisterTask(cronKind, cj.ID)
	}
	return s.repo.DeleteCronJob(cj.ID)
}

// RunCronJobNow spawns a Job immediately from a CronJob's template (ignores the
// concurrency policy — a manual trigger always runs).
func (s *Service) RunCronJobNow(ctx context.Context, workspaceID, id uint, triggeredBy *uint) (*models.Job, error) {
	cj, err := s.repo.FindCronJobInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrCronNotFound
	}
	return s.spawnJob(ctx, cj, models.JobSourceManual, triggeredBy)
}

// schedule (re)registers a cronjob's tick with the scheduler.
func (s *Service) schedule(cj *models.CronJob) {
	if s.scheduler == nil {
		return
	}
	id := cj.ID
	name := cj.Name
	if name == "" {
		name = "CronJob"
	}
	if err := s.scheduler.RegisterTask(cronKind, id, name, cj.Schedule, func() error {
		return s.tick(id)
	}); err != nil {
		logger.Error("invalid cronjob schedule", "cronjob", id, "schedule", cj.Schedule, "error", err)
	}
}

// tick runs on each scheduled fire: honors the concurrency policy, spawns a Job,
// updates LastRunAt, and prunes history.
func (s *Service) tick(id uint) error {
	cj, err := s.repo.FindCronJobByID(id)
	if err != nil {
		return err
	}
	if !cj.Enabled {
		return nil
	}
	active, _ := s.repo.ActiveByCronJob(cj.ID)
	switch cj.ConcurrencyPolicy {
	case models.ConcurrencyForbid:
		if len(active) > 0 {
			logger.Info("cronjob tick skipped (forbid policy, run still active)", "cronjob", cj.ID)
			return nil
		}
	case models.ConcurrencyReplace:
		for i := range active {
			_ = s.cancelJob(context.Background(), &active[i])
		}
	}
	if _, err := s.spawnJob(context.Background(), cj, models.JobSourceScheduled, nil); err != nil {
		return err
	}
	return nil
}

// spawnJob creates and enqueues a Job from a cronjob's template, then records the
// run and prunes old history.
func (s *Service) spawnJob(ctx context.Context, cj *models.CronJob, source string, triggeredBy *uint) (*models.Job, error) {
	j, err := s.Run(ctx, cj.WorkspaceID, cj.ApplicationID, RunRequest{
		Name:        cj.Name,
		Command:     cj.Command,
		Entrypoint:  cj.Entrypoint,
		Image:       cj.Image,
		RegistryID:  cj.RegistryID,
		RunAsUser:   cj.RunAsUser, // blank inherits the app's, resolved by Run
		TimeoutSecs: cj.TimeoutSecs,
		Source:      source,
		TriggeredBy: triggeredBy,
		CronJobID:   &cj.ID,
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	cj.LastRunAt = &now
	_ = s.repo.UpdateCronJob(cj)
	keep := cj.HistoryLimit
	if keep <= 0 {
		keep = defaultHistoryLimit
	}
	_ = s.repo.PruneCronJobHistory(cj.ID, keep)
	return j, nil
}

func normalizePolicy(p string) string {
	switch p {
	case models.ConcurrencyForbid, models.ConcurrencyReplace:
		return p
	default:
		return models.ConcurrencyAllow
	}
}

func validateCron(expr string) error {
	if expr == "" {
		return ErrInvalidSchedule
	}
	_, err := cron.ParseStandard(expr)
	return err
}
