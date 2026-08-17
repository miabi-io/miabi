// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package job runs one-off commands in an application's runtime context (a Job)
// and the schedules that spawn them (a CronJob). This service is the request
// side: it creates Job rows and enqueues them; the worker executes them.
package job

import (
	"context"
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound    = errors.New("job not found")
	ErrNoCommand   = errors.New("a command is required")
	ErrNoImage     = errors.New("application has no image yet; deploy it first")
	ErrNotTerminal = errors.New("job is still active; cancel it before deleting")
	ErrAlreadyDone = errors.New("job has already finished")
	ErrNotRunning  = errors.New("job is not running")
)

// Enqueuer schedules background execution. Implemented by the worker producer.
type Enqueuer interface {
	EnqueueRunJob(jobID, serverID uint) error
}

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

type Service struct {
	repo      *repositories.JobRepository
	apps      *repositories.ApplicationRepository
	releases  *repositories.ReleaseRepository
	clients   NodeDocker
	enqueuer  Enqueuer
	scheduler Scheduler
	quota     *quota.Service
	// defaultTimeoutSecs caps a job's runtime when the request leaves it unset.
	defaultTimeoutSecs int
}

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

func NewService(repo *repositories.JobRepository, apps *repositories.ApplicationRepository, releases *repositories.ReleaseRepository, clients NodeDocker, enqueuer Enqueuer, defaultTimeoutSecs int) *Service {
	if defaultTimeoutSecs <= 0 {
		defaultTimeoutSecs = 3600
	}
	return &Service{repo: repo, apps: apps, releases: releases, clients: clients, enqueuer: enqueuer, defaultTimeoutSecs: defaultTimeoutSecs}
}

// RunRequest describes a one-off command to run against an app.
type RunRequest struct {
	Name        string
	Command     []string
	Entrypoint  []string
	TimeoutSecs int
	// Image optionally overrides the app's active-release image (a custom image
	// run in the app's runtime context). RegistryID authenticates its pull and
	// defaults to the app's registry when nil.
	Image      string
	RegistryID *uint
	// RunAsUser pins this run to an account ("uid", "uid:gid", "name",
	// "name:group"); empty inherits the app's. A one-off often needs different
	// privileges from the long-running process — a migration writing a volume the
	// app only reads, say.
	RunAsUser   string
	Source      string // manual | api | scheduled
	TriggeredBy *uint  // user id (nil for scheduled)
	CronJobID   *uint  // set when spawned by a cronjob
}

// Run creates a Job in the app's runtime context and enqueues it. The image is
// snapshotted from the app's active release (or its image ref) so history shows
// exactly what ran.
func (s *Service) Run(_ context.Context, workspaceID, appID uint, req RunRequest) (*models.Job, error) {
	if len(req.Command) == 0 {
		return nil, ErrNoCommand
	}
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, ErrNotFound
	}
	// A custom image runs in the app's runtime context and must be pulled (it may
	// be private/absent); default its registry to the app's. Otherwise run the
	// app's active-release image, already present on its node.
	var (
		image      string
		registryID *uint
		pull       bool
	)
	if strings.TrimSpace(req.Image) != "" {
		image = strings.TrimSpace(req.Image)
		pull = true
		registryID = req.RegistryID
		if registryID == nil {
			registryID = app.RegistryID
		}
	} else if image, err = s.resolveImage(app); err != nil {
		return nil, err
	}
	// Snapshot the account this run is pinned to (the request's, else the app's) so history shows
	// what actually ran, and validate it here rather than letting the worker fail the run.
	runAsUser, err := s.validRunAsUser(app, req.RunAsUser)
	if err != nil {
		return nil, err
	}
	timeout := req.TimeoutSecs
	if timeout <= 0 {
		timeout = s.defaultTimeoutSecs
	}
	source := req.Source
	if source == "" {
		source = models.JobSourceManual
	}
	j := &models.Job{
		WorkspaceID:   workspaceID,
		ApplicationID: appID,
		ServerID:      app.ServerID,
		CronJobID:     req.CronJobID,
		Name:          req.Name,
		Command:       req.Command,
		Entrypoint:    req.Entrypoint,
		Image:         image,
		RegistryID:    registryID,
		Pull:          pull,
		RunAsUser:     runAsUser,
		Status:        models.JobPending,
		TimeoutSecs:   timeout,
		Source:        source,
		TriggeredByID: req.TriggeredBy,
	}
	if err := s.repo.Create(j); err != nil {
		return nil, err
	}
	if err := s.enqueuer.EnqueueRunJob(j.ID, j.ServerID); err != nil {
		return nil, err
	}
	return j, nil
}

// validRunAsUser resolves the account a run is pinned to — the request's choice, else the app's — and
// checks it against the workspace's security profile. A job runs in the app's runtime context with
// the app's data, so it cannot be the loophole that gets root where a deploy could not.
func (s *Service) validRunAsUser(app *models.Application, requested string) (string, error) {
	v, err := s.checkRunAsUser(app, requested)
	if err != nil || v != "" {
		return v, err
	}
	return app.RunAsUser, nil
}

// checkRunAsUser normalizes a requested run-as user and rejects one that would escape a workspace's
// non-root mandate. Blank passes through blank, so a caller decides what "unset" inherits from.
func (s *Service) checkRunAsUser(app *models.Application, requested string) (string, error) {
	v, err := models.NormalizeRunAsUser(requested)
	if err != nil || v == "" {
		return "", err
	}
	if s.quota.RequireNonRootUser(app.WorkspaceID, app.OfficialTemplate) && !models.RunAsUserIsNonRoot(v) {
		return "", models.ErrRunAsUserRoot
	}
	return v, nil
}

// resolveImage picks the image a job should run: the app's active release image
// when present (exactly what's deployed), else the configured image ref for
// image-source apps. Git-source apps with no release yet cannot run a job.
func (s *Service) resolveImage(app *models.Application) (string, error) {
	if rel, err := s.releases.FindActive(app.ID); err == nil && rel.Image != "" {
		return rel.Image, nil
	}
	if app.SourceType == models.AppSourceImage {
		if ref := app.ImageRef(""); ref != "" {
			return ref, nil
		}
	}
	return "", ErrNoImage
}

func (s *Service) Get(workspaceID, id uint) (*models.Job, error) {
	j, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return j, nil
}

// List returns the workspace's job history (optionally filtered to appID),
// annotated with each job's application name for the workspace-level view.
func (s *Service) List(workspaceID, appID uint, limit int) ([]models.Job, error) {
	jobs, err := s.repo.ListByWorkspace(workspaceID, appID, limit)
	if err != nil {
		return nil, err
	}
	names := s.appNames(workspaceID)
	for i := range jobs {
		jobs[i].AppName = names[jobs[i].ApplicationID]
	}
	return jobs, nil
}

// appNames maps application id -> name for a workspace (best-effort; empty on error).
func (s *Service) appNames(workspaceID uint) map[uint]string {
	out := map[uint]string{}
	apps, err := s.apps.ListByWorkspace(workspaceID)
	if err != nil {
		return out
	}
	for i := range apps {
		out[apps[i].ID] = apps[i].Name
	}
	return out
}

// Cancel stops a running job by force-removing its container; the worker, on
// completion, sees the canceled status and leaves it. A pending (not yet
// started) job is marked canceled directly.
func (s *Service) Cancel(ctx context.Context, workspaceID, id uint) error {
	j, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	if j.Status.IsTerminal() {
		return ErrAlreadyDone
	}
	return s.cancelJob(ctx, j)
}

// cancelJob marks a job canceled and force-removes its container so the worker's
// RunOneShot unblocks; on completion the worker sees the canceled state.
func (s *Service) cancelJob(ctx context.Context, j *models.Job) error {
	j.Status = models.JobCanceled
	if err := s.repo.Update(j); err != nil {
		return err
	}
	if j.ContainerID != "" {
		if dc, derr := s.clients.For(j.ServerID); derr == nil {
			_ = dc.RemoveContainer(ctx, j.ContainerID, true)
		}
	}
	return nil
}

// Delete removes a terminal job's record.
func (s *Service) Delete(workspaceID, id uint) error {
	j, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	if !j.Status.IsTerminal() {
		return ErrNotTerminal
	}
	return s.repo.Delete(j.ID)
}
