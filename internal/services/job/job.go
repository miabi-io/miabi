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

// Package job is the request side of Jobs and CronJobs: it creates rows and enqueues them for the worker.
package job

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

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
	// ErrVolumesSplit means no single node holds every node-local volume the app mounts.
	ErrVolumesSplit = errors.New("the app's node-local volumes live on different nodes")
	// ErrNodeVolumeConflict refuses a job pin away from a node-local volume, where the job would see an empty one.
	ErrNodeVolumeConflict = errors.New("NODE_VOLUME_CONFLICT")
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
	repo *repositories.JobRepository
	// paused reports a suspended workspace, whose CronJob schedules are dropped until it is lifted.
	paused func(workspaceID uint) bool
	// deployments finds an app's in-flight deploy, which a declared run waits for.
	deployments *repositories.DeploymentRepository
	apps        *repositories.ApplicationRepository
	releases    *repositories.ReleaseRepository
	clients     NodeDocker
	volumes     VolumeLookup
	nodeName    func(serverID uint) string
	enqueuer    Enqueuer
	scheduler   Scheduler
	quota       *quota.Service
	// Tracks what this process registered so SyncSchedules can see changes made elsewhere.
	regMu      sync.Mutex
	registered map[uint]string
	// defaultTimeoutSecs caps a job's runtime when the request leaves it unset.
	defaultTimeoutSecs int
}

// VolumeLookup finds the volumes an app mounts that live on a single node.
type VolumeLookup interface {
	NodeLocalOf(app *models.Application) []*models.Volume
}

// NodeNames returns a node's fixed name, the value placement.node takes; server id 0 is the control plane.
func NodeNames(servers *repositories.ServerRepository) func(uint) string {
	return func(id uint) string {
		var srv *models.Server
		var err error
		if id == 0 {
			srv, err = servers.FindLocal()
		} else {
			srv, err = servers.FindByID(id)
		}
		if err != nil {
			return ""
		}
		return srv.Name
	}
}

// SetVolumes wires the volume lookup; nil runs every job on the app's node.
func (s *Service) SetVolumes(v VolumeLookup, nodeName func(serverID uint) string) {
	s.volumes, s.nodeName = v, nodeName
}

// nil runs declared Jobs straight away.
func (s *Service) SetDeployments(d *repositories.DeploymentRepository) { s.deployments = d }

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// SetPaused wires the suspended-workspace check used by SyncSchedules.
func (s *Service) SetPaused(f func(workspaceID uint) bool) { s.paused = f }

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
	// RegistryID defaults to the app's registry when nil.
	Image      string
	RegistryID *uint
	// Empty inherits the app's; a one-off may need different privileges, e.g. a migration writing a volume.
	RunAsUser string
	// Env is already sealed (SealEnv); InheritEnv nil means true.
	Env         models.JobEnv
	InheritEnv  *bool
	Source      string // manual | api | scheduled
	TriggeredBy *uint  // user id (nil for scheduled)
	CronJobID   *uint  // set when spawned by a cronjob
	// JobDefinitionID is set for a run of a declared Job.
	JobDefinitionID *uint
	// The image is resolved from the release that deployment activated.
	WaitDeploymentID *uint
	// ServerID pins the run to a node (0 = follow the app and its node-local volumes).
	ServerID uint
	// Attempt numbers a retry of a declared Job's run; 0 means the first.
	Attempt int
}

// A service app's ServerID is only the manager it was created through; its node-local volumes are on the node its tasks run on.
func (s *Service) jobNode(app *models.Application, pinned uint) (uint, error) {
	if pinned != 0 {
		return pinned, s.checkPin(app, pinned)
	}
	if app.RuntimeKind != models.RuntimeService {
		return app.ServerID, nil
	}
	// A pinned service runs on its pin, which PinNode already keeps on the node of its node-local volumes.
	if app.PinnedServerID != 0 {
		return app.PinnedServerID, nil
	}
	if s.volumes == nil {
		return app.ServerID, nil
	}
	vols := s.volumes.NodeLocalOf(app)
	if len(vols) == 0 {
		return app.ServerID, nil
	}
	node := vols[0].ServerID
	for _, v := range vols[1:] {
		if v.ServerID != node {
			return 0, fmt.Errorf("%w: %s", ErrVolumesSplit, s.volumeNodes(vols))
		}
	}
	return node, nil
}

// Any runtime: a job pinned away from the app's node-local volume would get a new, empty volume of the same name.
func (s *Service) checkPin(app *models.Application, pinned uint) error {
	if pinned == 0 || s.volumes == nil {
		return nil
	}
	for _, v := range s.volumes.NodeLocalOf(app) {
		if v.ServerID != pinned {
			return fmt.Errorf("%w: volume %q of app %q lives on %s; a job pinned to %s would not see its data",
				ErrNodeVolumeConflict, v.Name, app.Name, s.name(v.ServerID), s.name(pinned))
		}
	}
	return nil
}

func (s *Service) name(serverID uint) string {
	if s.nodeName != nil {
		if n := s.nodeName(serverID); n != "" {
			return n
		}
	}
	return fmt.Sprintf("node %d", serverID)
}

func (s *Service) volumeNodes(vols []*models.Volume) string {
	parts := make([]string, 0, len(vols))
	for _, v := range vols {
		where := fmt.Sprintf("node %d", v.ServerID)
		if s.nodeName != nil {
			if n := s.nodeName(v.ServerID); n != "" {
				where = n
			}
		}
		parts = append(parts, fmt.Sprintf("%q on %s", v.Name, where))
	}
	return strings.Join(parts, ", ")
}

// Run snapshots the image from the active release so history shows exactly what ran.
func (s *Service) Run(_ context.Context, workspaceID, appID uint, req RunRequest) (*models.Job, error) {
	if len(req.Command) == 0 {
		return nil, ErrNoCommand
	}
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, ErrNotFound
	}
	// A custom image may be private or absent so it is pulled; the active-release image is already on the node.
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
	} else if image, err = s.resolveImage(app); err != nil && req.WaitDeploymentID == nil {
		// A run waiting on a deploy takes its image from the release that deploy activates.
		return nil, err
	}
	// Validated here rather than letting the worker fail the run.
	runAsUser, err := s.validRunAsUser(app, req.RunAsUser)
	if err != nil {
		return nil, err
	}
	serverID, err := s.jobNode(app, req.ServerID)
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
		WorkspaceID:      workspaceID,
		ApplicationID:    appID,
		ServerID:         serverID,
		CronJobID:        req.CronJobID,
		JobDefinitionID:  req.JobDefinitionID,
		WaitDeploymentID: req.WaitDeploymentID,
		Attempt:          max(req.Attempt, 1),
		Name:             req.Name,
		Command:          req.Command,
		Entrypoint:       req.Entrypoint,
		Image:            image,
		RegistryID:       registryID,
		Pull:             pull,
		RunAsUser:        runAsUser,
		Env:              req.Env,
		InheritEnv:       inheritOrDefault(req.InheritEnv),
		Status:           models.JobPending,
		TimeoutSecs:      timeout,
		Source:           source,
		TriggeredByID:    req.TriggeredBy,
	}
	if err := s.repo.Create(j); err != nil {
		return nil, err
	}
	if err := s.enqueuer.EnqueueRunJob(j.ID, j.ServerID); err != nil {
		return nil, err
	}
	return j, nil
}

// A job runs with the app's data, so it must not be the loophole that gets root where a deploy could not.
func (s *Service) validRunAsUser(app *models.Application, requested string) (string, error) {
	v, err := s.checkRunAsUser(app, requested)
	if err != nil || v != "" {
		return v, err
	}
	return app.RunAsUser, nil
}

// Blank passes through so the caller decides what "unset" inherits from.
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

// Git-source apps with no release yet cannot run a job.
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
	j.Node = s.pinName(j.ServerID)
	return j, nil
}

func (s *Service) pinName(serverID uint) string {
	if s.nodeName == nil {
		return ""
	}
	return s.nodeName(serverID)
}

func (s *Service) List(workspaceID, appID uint, limit int) ([]models.Job, error) {
	jobs, err := s.repo.ListByWorkspace(workspaceID, appID, limit)
	if err != nil {
		return nil, err
	}
	names := s.appNames(workspaceID)
	for i := range jobs {
		jobs[i].AppName = names[jobs[i].ApplicationID]
		jobs[i].Node = s.pinName(jobs[i].ServerID)
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

// Cancel force-removes a running job's container; the worker sees the canceled status and leaves it.
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

// Force-removing the container unblocks the worker's RunOneShot.
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
