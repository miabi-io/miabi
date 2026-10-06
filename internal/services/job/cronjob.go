// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

const cronKind = "cronjob"

// defaultHistoryLimit caps spawned-job history per cronjob when unset.
const defaultHistoryLimit = 20

var (
	ErrInvalidSchedule = errors.New("invalid cron schedule")
	ErrCronNotFound    = errors.New("cronjob not found")
	ErrCronNameTaken   = errors.New("a cronjob with this name already exists in the workspace")
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
	// Name is slugified into the workspace-unique handle; blank generates one.
	Name        string
	DisplayName string
	Schedule    string
	Command     []string
	Entrypoint  []string
	Image       string // optional custom image override (blank = app's release)
	RegistryID  *uint
	// RunAsUser pins spawned runs to an account (blank = inherit the app's).
	RunAsUser         string
	TimeoutSecs       int
	Enabled           bool
	ConcurrencyPolicy string
	HistoryLimit      int
	// Metadata and Annotations replace the stored ones when non-nil; nil keeps
	// them, so a console edit never drops the GitOps ownership labels.
	Metadata    models.Metadata
	Annotations models.Metadata
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
	name, display, err := s.cronName(workspaceID, in.Name, in.DisplayName, 0)
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
		Name:              name,
		DisplayName:       display,
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
		Metadata:          in.Metadata,
		Annotations:       in.Annotations,
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
	if strings.TrimSpace(in.Name) != "" || strings.TrimSpace(in.DisplayName) != "" {
		raw := in.Name
		if strings.TrimSpace(raw) == "" {
			raw = cj.Name
		}
		name, display, err := s.cronName(workspaceID, raw, in.DisplayName, cj.ID)
		if err != nil {
			return nil, err
		}
		cj.Name = name
		if display != "" {
			cj.DisplayName = display
		}
	}
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
	if in.Metadata != nil {
		cj.Metadata = in.Metadata
	}
	if in.Annotations != nil {
		cj.Annotations = in.Annotations
	}
	if err := s.repo.UpdateCronJob(cj); err != nil {
		return nil, err
	}
	// Re-register to pick up schedule/command changes, or unregister if disabled.
	if cj.Enabled {
		s.schedule(cj)
	} else {
		s.unschedule(cj.ID)
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

// GetCronJobByName loads a cronjob by its workspace-unique name.
func (s *Service) GetCronJobByName(workspaceID uint, name string) (*models.CronJob, error) {
	cj, err := s.repo.FindCronJobByName(workspaceID, name)
	if err != nil {
		return nil, ErrCronNotFound
	}
	return cj, nil
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
	s.unschedule(cj.ID)
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
	name := cj.DisplayName
	if name == "" {
		name = cj.Name
	}
	if err := s.scheduler.RegisterTask(cronKind, id, name, cj.Schedule, func() error {
		return s.tick(id)
	}); err != nil {
		logger.Error("invalid cronjob schedule", "cronjob", id, "schedule", cj.Schedule, "error", err)
		return
	}
	s.regMu.Lock()
	if s.registered == nil {
		s.registered = map[uint]string{}
	}
	s.registered[id] = registration(cj)
	s.regMu.Unlock()
}

func (s *Service) unschedule(id uint) {
	if s.scheduler == nil {
		return
	}
	s.scheduler.UnregisterTask(cronKind, id)
	s.regMu.Lock()
	delete(s.registered, id)
	s.regMu.Unlock()
}

// registration is what a registered tick depends on; a change means re-register.
func registration(cj *models.CronJob) string {
	return cj.Schedule + "\x00" + cj.Name + "\x00" + cj.DisplayName
}

// SyncSchedules reconciles this process's registered ticks with the database. A
// create, edit or delete registers only in the process that served it, and only
// the leader runs ticks, so the leader calls this periodically to pick up
// changes made on other replicas.
func (s *Service) SyncSchedules() error {
	if s.scheduler == nil {
		return nil
	}
	list, err := s.repo.ListEnabledCronJobs()
	if err != nil {
		return err
	}
	want := make(map[uint]bool, len(list))
	for i := range list {
		cj := &list[i]
		want[cj.ID] = true
		s.regMu.Lock()
		current, ok := s.registered[cj.ID]
		s.regMu.Unlock()
		if !ok || current != registration(cj) {
			s.schedule(cj)
		}
	}
	s.regMu.Lock()
	var stale []uint
	for id := range s.registered {
		if !want[id] {
			stale = append(stale, id)
		}
	}
	s.regMu.Unlock()
	for _, id := range stale {
		s.unschedule(id)
	}
	return nil
}

// cronName resolves a requested name into the workspace-unique slug and the
// display name to store. A blank name gets a generated one; a name that slugs
// differently keeps the original as its display name.
func (s *Service) cronName(workspaceID uint, name, display string, selfID uint) (string, string, error) {
	raw := strings.TrimSpace(name)
	display = strings.TrimSpace(display)
	taken := func(candidate string) (bool, error) {
		cj, err := s.repo.FindCronJobByName(workspaceID, candidate)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return cj.ID != selfID, nil
	}
	if raw == "" {
		n, err := slug.Unique("cronjob", "cronjob", taken)
		return n, display, err
	}
	n := slug.Make(raw, "cronjob")
	if display == "" && n != raw {
		display = raw
	}
	exists, err := taken(n)
	if err != nil {
		return "", "", err
	}
	if exists {
		return "", "", ErrCronNameTaken
	}
	return n, display, nil
}

// tick runs on each scheduled fire: honors the concurrency policy, spawns a Job,
// updates LastRunAt, and prunes history.
func (s *Service) tick(id uint) error {
	cj, err := s.repo.FindCronJobByID(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Deleted on another replica or with its app: stop firing.
		s.unschedule(id)
		return nil
	}
	if err != nil {
		return err
	}
	if !cj.Enabled {
		s.unschedule(id)
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
	_ = s.repo.UpdateCronJobLastRun(cj.ID, now)
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

// OnlyToggles reports whether applying in to cj would change nothing but
// whether it is enabled: the one edit a GitOps-owned CronJob accepts from the
// console, since the manifest leaves an unstated suspend alone.
func (in CronJobInput) OnlyToggles(cj *models.CronJob) bool {
	user, err := models.NormalizeRunAsUser(in.RunAsUser)
	if err != nil {
		return false
	}
	sameRegistry := (in.RegistryID == nil && cj.RegistryID == nil) ||
		(in.RegistryID != nil && cj.RegistryID != nil && *in.RegistryID == *cj.RegistryID)
	sameName := strings.TrimSpace(in.Name) == "" || slug.Make(in.Name, "cronjob") == cj.Name
	return sameName && sameRegistry && in.Schedule == cj.Schedule &&
		slices.Equal(in.Command, cj.Command) && slices.Equal(in.Entrypoint, cj.Entrypoint) &&
		strings.TrimSpace(in.Image) == strings.TrimSpace(cj.Image) && user == cj.RunAsUser &&
		in.TimeoutSecs == cj.TimeoutSecs && normalizePolicy(in.ConcurrencyPolicy) == cj.ConcurrencyPolicy &&
		in.HistoryLimit == cj.HistoryLimit
}
