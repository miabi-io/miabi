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

// Package stack groups apps organizationally; deleting a stack detaches its apps, which keep running.
package stack

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/dotenv"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/netalloc"
	"github.com/miabi-io/miabi/internal/services/portbinding"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNameRequired = errors.New("stack name is required")
	ErrNameTaken    = errors.New("a stack with this name already exists")
	// The name derives DockerName, network and volumes, and GitOps matches on it; use DisplayName for a label.
	ErrNameImmutable   = errors.New("a stack's name cannot be changed; edit its display name instead")
	ErrNotFound        = errors.New("stack not found")
	ErrAppNotFound     = errors.New("application not found in workspace")
	ErrAppInOtherStack = errors.New("application already belongs to another stack")
	ErrInvalidAction   = errors.New("invalid stack action")
	ErrKeyRequired     = errors.New("environment variable key is required")
	ErrEnvVarNotFound  = errors.New("environment variable not found")
	ErrAppsRunning     = errors.New("stop the stack before deleting it with its applications")
)

// An interface keeps the stack service unit-testable with a fake.
type AppService interface {
	Start(ctx context.Context, app *models.Application) (*models.Deployment, error)
	Stop(ctx context.Context, app *models.Application) error
	Restart(ctx context.Context, app *models.Application) (*models.Deployment, error)
	WaitRunning(ctx context.Context, app *models.Application) error
	Deploy(app *models.Application, registryOverride *uint, tagOverride string, strategy models.DeployStrategy) (*models.Deployment, error)
	MarkRedeployRequired(app *models.Application) (bool, error)
	Delete(ctx context.Context, app *models.Application) error
	LiveStatus(ctx context.Context, app *models.Application) application.LiveStatus
	Create(workspaceID uint, in application.CreateInput) (*models.Application, error)
	SetEnvVar(appID uint, key, value string, isSecret bool) error
	AttachVolume(app *models.Application, volumeID uint, path string) error
}

type VolumeCreator interface {
	Create(ctx context.Context, workspaceID, serverID uint, name string, sizeBytes int64, meta, annotations models.Metadata) (*models.Volume, error)
}

type NetworkManager interface {
	EnsureNetwork(ctx context.Context, name string) (string, error)
	RemoveNetwork(ctx context.Context, name string) error
	EnsureNetworkSpec(ctx context.Context, spec docker.NetworkSpec) (string, error)
	ListNetworks(ctx context.Context) ([]docker.Network, error)
}

type PortRequester interface {
	Request(workspaceID, userID uint, in portbinding.RequestInput) (*models.PortBinding, error)
	RequestImport(workspaceID, userID uint, in portbinding.RequestInput) (*models.PortBinding, string, error)
}

type Service struct {
	repo    *repositories.StackRepository
	apps    *repositories.ApplicationRepository
	env     *repositories.StackEnvVarRepository
	events  *repositories.AppEventRepository
	app     AppService
	volumes VolumeCreator
	docker  NetworkManager
	ports   PortRequester
	alloc   *netalloc.Service
}

func NewService(repo *repositories.StackRepository, apps *repositories.ApplicationRepository, env *repositories.StackEnvVarRepository, events *repositories.AppEventRepository, app AppService, volumes VolumeCreator, docker NetworkManager, ports PortRequester) *Service {
	return &Service{repo: repo, apps: apps, env: env, events: events, app: app, volumes: volumes, docker: docker, ports: ports}
}

// Nil falls back to Docker's default address pool.
func (s *Service) SetAllocator(a *netalloc.Service) { s.alloc = a }

type Input struct {
	Name        string
	DisplayName string
	Description string
	// Callers may set reserved "miabi.io/" provenance keys; a missing managed-by defaults to "user".
	Metadata    models.Metadata
	Annotations models.Metadata
	// ClusterID is the location the stack's members run in; 0 is the default cluster.
	ClusterID uint
}

func (s *Service) Create(ctx context.Context, workspaceID uint, in Input) (*models.Stack, error) {
	name := slug.Make(in.Name, "")
	if name == "" {
		return nil, ErrNameRequired
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(in.Name)
	}
	taken, err := s.repo.ExistsByName(workspaceID, name)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrNameTaken
	}
	dockerName, err := s.uniqueDockerName(workspaceID, name)
	if err != nil {
		return nil, err
	}
	dockerNetwork := "mb-stack-" + dockerName
	if s.docker != nil {
		spec := docker.NetworkSpec{Name: dockerNetwork}
		var err error
		if s.alloc != nil {
			// A Miabi-carved subnet avoids exhausting Docker's small built-in default-address-pools.
			_, _, err = s.alloc.EnsureManaged(ctx, s.docker, spec, 0, models.NetAllocKindStack)
		} else {
			_, err = s.docker.EnsureNetworkSpec(ctx, spec)
		}
		if err != nil {
			return nil, fmt.Errorf("create stack network: %w", err)
		}
	}
	st := &models.Stack{
		WorkspaceID:   workspaceID,
		Name:          name,
		DisplayName:   displayName,
		DockerName:    dockerName,
		DockerNetwork: dockerNetwork,
		Description:   strings.TrimSpace(in.Description),
		Metadata:      models.DefaultManagedBy(in.Metadata, models.ManagedByUser),
		Annotations:   in.Annotations,
		ClusterID:     in.ClusterID,
	}
	if err := s.repo.Create(st); err != nil {
		if s.docker != nil {
			_ = s.docker.RemoveNetwork(ctx, dockerNetwork) // roll back the docker side
		}
		return nil, err
	}
	return st, nil
}

func (s *Service) uniqueDockerName(workspaceID uint, name string) (string, error) {
	base := fmt.Sprintf("ws%d-%s", workspaceID, slug.Make(name, "stack"))
	return slug.Unique(base, base, func(candidate string) (bool, error) {
		return s.repo.ExistsByDockerName(candidate)
	})
}

func (s *Service) Get(workspaceID, id uint) (*models.Stack, error) {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return st, nil
}

func (s *Service) List(workspaceID uint) ([]models.Stack, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

// AppCount returns the number of applications in a stack.
func (s *Service) AppCount(stackID uint) (int64, error) {
	return s.repo.CountApps(stackID)
}

type UpdateInput struct {
	// The current name is a no-op so whole-object PATCH clients work; a different one is refused.
	Name        *string
	DisplayName *string
	Description *string
	// Annotations, when non-nil, replaces the stack's annotations wholesale.
	Annotations models.Metadata
}

func (s *Service) Update(workspaceID, id uint, in UpdateInput) (*models.Stack, error) {
	st, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if in.Name != nil {
		name := slug.Make(*in.Name, "")
		if name == "" {
			return nil, ErrNameRequired
		}
		if name != st.Name {
			return nil, ErrNameImmutable
		}
	}
	if in.DisplayName != nil {
		display := strings.TrimSpace(*in.DisplayName)
		if display == "" {
			display = st.Name
		}
		st.DisplayName = display
	}
	if in.Description != nil {
		st.Description = strings.TrimSpace(*in.Description)
	}
	if in.Annotations != nil {
		st.Annotations = in.Annotations
	}
	if err := s.repo.Update(st); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *Service) Delete(ctx context.Context, workspaceID, id uint, withApps bool) error {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	if withApps {
		if running := s.runningApps(ctx, st.Apps); len(running) > 0 {
			return fmt.Errorf("%w (running: %s)", ErrAppsRunning, strings.Join(running, ", "))
		}
		for i := range st.Apps {
			if derr := s.app.Delete(ctx, &st.Apps[i]); derr != nil {
				return derr
			}
		}
	} else if err := s.repo.DetachApps(st.ID); err != nil {
		return err
	}
	if err := s.repo.Delete(st.ID); err != nil {
		return err
	}
	// Removing the network fails while detached apps still run on it; it's reclaimed once they leave.
	if s.docker != nil && st.DockerNetwork != "" {
		_ = s.docker.RemoveNetwork(ctx, st.DockerNetwork)
		if s.alloc != nil {
			_ = s.alloc.Release(st.DockerNetwork) // return the subnet to the pool
		}
	}
	return nil
}

// An app already in a different stack is rejected rather than silently moved.
func (s *Service) runningApps(ctx context.Context, apps []models.Application) []string {
	var names []string
	for i := range apps {
		if s.app.LiveStatus(ctx, &apps[i]).Running {
			names = append(names, apps[i].Name)
		}
	}
	return names
}

func (s *Service) AddApp(workspaceID, stackID, appID uint) (*models.Stack, error) {
	st, err := s.repo.FindInWorkspace(workspaceID, stackID)
	if err != nil {
		return nil, ErrNotFound
	}
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, ErrAppNotFound
	}
	if app.StackID != nil && *app.StackID != st.ID {
		return nil, ErrAppInOtherStack
	}
	if err := s.apps.SetStack(app.ID, &st.ID); err != nil {
		return nil, err
	}
	_, _ = s.app.MarkRedeployRequired(app) // apply the new compose labels on next deploy
	return s.repo.FindInWorkspaceWithApps(workspaceID, st.ID)
}

func (s *Service) RemoveApp(workspaceID, stackID, appID uint) (*models.Stack, error) {
	st, err := s.repo.FindInWorkspace(workspaceID, stackID)
	if err != nil {
		return nil, ErrNotFound
	}
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, ErrAppNotFound
	}
	if app.StackID != nil && *app.StackID == st.ID {
		if err := s.apps.SetStack(app.ID, nil); err != nil {
			return nil, err
		}
		_, _ = s.app.MarkRedeployRequired(app) // drop the compose labels on next deploy
	}
	return s.repo.FindInWorkspaceWithApps(workspaceID, st.ID)
}

// Action is a stack-wide container lifecycle operation.
type Action string

const (
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
)

type AppActionResult struct {
	AppID   uint   `json:"app_id"`
	AppName string `json:"app_name"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

// Lifecycle is best-effort per app; with rolling, each app runs before the next so the stack never fully stops.
func (s *Service) Lifecycle(ctx context.Context, workspaceID, stackID uint, action Action, rolling bool) ([]AppActionResult, error) {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, stackID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.applyLifecycle(ctx, st.Apps, action, rolling)
}

// Split out so per-app outcome logic is testable without a database.
func (s *Service) applyLifecycle(ctx context.Context, apps []models.Application, action Action, rolling bool) ([]AppActionResult, error) {
	results := make([]AppActionResult, 0, len(apps))
	for i := range apps {
		app := &apps[i]
		res := AppActionResult{AppID: app.ID, AppName: app.Name, Status: "ok"}
		var aerr error
		switch action {
		case ActionStart:
			_, aerr = s.app.Start(ctx, app)
		case ActionStop:
			aerr = s.app.Stop(ctx, app)
		case ActionRestart:
			if _, aerr = s.app.Restart(ctx, app); aerr == nil && rolling {
				aerr = s.app.WaitRunning(ctx, app) // readiness gate before the next app
			}
		default:
			return nil, ErrInvalidAction
		}
		switch {
		case aerr == nil:
		case errors.Is(aerr, application.ErrNotDeployable):
			res.Status = "skipped"
			res.Error = "no active release"
		default:
			res.Status = "failed"
			res.Error = aerr.Error()
		}
		results = append(results, res)
	}
	return results, nil
}

// DeployResult reports the outcome of a deploy for one member app.
type DeployResult struct {
	AppID        uint   `json:"app_id"`
	AppName      string `json:"app_name"`
	Status       string `json:"status"` // queued | failed
	DeploymentID uint   `json:"deployment_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

func (s *Service) DeployAll(workspaceID, stackID uint) ([]DeployResult, error) {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, stackID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.deployApps(st.Apps, false), nil
}

func (s *Service) deployApps(apps []models.Application, onlyOutdated bool) []DeployResult {
	results := make([]DeployResult, 0, len(apps))
	for i := range apps {
		app := &apps[i]
		if onlyOutdated && !app.RedeployRequired {
			continue
		}
		res := DeployResult{AppID: app.ID, AppName: app.Name, Status: "queued"}
		dep, derr := s.app.Deploy(app, nil, "", "")
		if derr != nil {
			res.Status, res.Error = "failed", derr.Error()
		} else {
			res.DeploymentID = dep.ID
		}
		results = append(results, res)
	}
	return results
}

// DeployOutdated leaves current and never-deployed apps alone, rather than restarting the whole stack.
func (s *Service) DeployOutdated(workspaceID, stackID uint) ([]DeployResult, error) {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, stackID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.deployApps(st.Apps, true), nil
}

// StatusCounts is an aggregate of member-app statuses for the health badge.
type StatusCounts struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Stopped int `json:"stopped"`
	Failed  int `json:"failed"`
	Other   int `json:"other"`
}

// AggregateStatus summarizes the statuses of a stack's member apps.
func (s *Service) AggregateStatus(stackID uint) (StatusCounts, error) {
	counts, err := s.repo.StatusCounts(stackID)
	if err != nil {
		return StatusCounts{}, err
	}
	var sc StatusCounts
	for status, n := range counts {
		sc.Total += n
		switch status {
		case models.AppStatusRunning:
			sc.Running += n
		case models.AppStatusStopped:
			sc.Stopped += n
		case models.AppStatusFailed:
			sc.Failed += n
		default:
			sc.Other += n
		}
	}
	return sc, nil
}

// Events returns the combined recent activity feed across the stack's apps.
func (s *Service) Events(workspaceID, stackID uint, limit int) ([]models.AppEvent, error) {
	if _, err := s.repo.FindInWorkspace(workspaceID, stackID); err != nil {
		return nil, ErrNotFound
	}
	ids, err := s.repo.AppIDs(stackID)
	if err != nil {
		return nil, err
	}
	return s.events.ListByApps(ids, limit)
}

func (s *Service) ListEnvVars(workspaceID, stackID uint) ([]models.StackEnvVar, error) {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, stackID)
	if err != nil {
		return nil, ErrNotFound
	}
	vars, err := s.env.ListByStack(stackID)
	if err != nil {
		return nil, err
	}
	overrides := s.overridesFor(st)
	for i := range vars {
		if vars[i].IsSecret {
			vars[i].Value = "••••••••"
		}
		vars[i].OverriddenBy = overrides[vars[i].Key]
	}
	return vars, nil
}

// An app-level variable wins, so without this a shared value is set, listed, and quietly ignored.
func (s *Service) overridesFor(st *models.Stack) map[string][]string {
	if len(st.Apps) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(st.Apps))
	names := make(map[uint]string, len(st.Apps))
	for i := range st.Apps {
		app := &st.Apps[i]
		ids = append(ids, app.ID)
		names[app.ID] = app.Name
	}
	owners, err := s.apps.EnvKeyOwners(ids)
	if err != nil {
		return nil // annotation is advisory; the list is still correct without it
	}
	out := make(map[string][]string, len(owners))
	for key, appIDs := range owners {
		for _, id := range appIDs {
			if n := names[id]; n != "" {
				out[key] = append(out[key], n)
			}
		}
		sort.Strings(out[key])
	}
	return out
}

// Reading plaintext is gated to workspace admins by the route and audited by the handler.
func (s *Service) RevealEnvVar(workspaceID, stackID uint, key string) (string, error) {
	if _, err := s.repo.FindInWorkspace(workspaceID, stackID); err != nil {
		return "", ErrNotFound
	}
	vars, err := s.env.ListByStack(stackID)
	if err != nil {
		return "", err
	}
	for _, v := range vars {
		if v.Key == key {
			if v.IsSecret {
				return crypto.Decrypt(v.Value)
			}
			return v.Value, nil
		}
	}
	return "", ErrEnvVarNotFound
}

func (s *Service) SetEnvVar(workspaceID, stackID uint, key, value string, isSecret bool) (int, error) {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, stackID)
	if err != nil {
		return 0, ErrNotFound
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, ErrKeyRequired
	}
	stored := value
	if isSecret {
		enc, err := crypto.EncryptWS(workspaceID, value)
		if err != nil {
			return 0, err
		}
		stored = enc
	}
	if err := s.env.Upsert(&models.StackEnvVar{StackID: stackID, Key: key, Value: stored, IsSecret: isSecret}); err != nil {
		return 0, err
	}
	return s.markMembersStale(st), nil
}

// Stack env is injected at deploy, so a change leaves running members silently behind without this.
func (s *Service) markMembersStale(st *models.Stack) int {
	n := 0
	for i := range st.Apps {
		if marked, err := s.app.MarkRedeployRequired(&st.Apps[i]); err == nil && marked {
			n++
		}
	}
	return n
}

func (s *Service) ImportEnvVars(workspaceID, stackID uint, content string, isSecret bool) (imported, stale int, err error) {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, stackID)
	if err != nil {
		return 0, 0, ErrNotFound
	}
	pairs := dotenv.Parse(content)
	for _, p := range pairs {
		stored := p.Value
		if isSecret {
			enc, encErr := crypto.EncryptWS(workspaceID, p.Value)
			if encErr != nil {
				return 0, 0, encErr
			}
			stored = enc
		}
		if uerr := s.env.Upsert(&models.StackEnvVar{StackID: stackID, Key: p.Key, Value: stored, IsSecret: isSecret}); uerr != nil {
			return 0, 0, uerr
		}
	}
	if len(pairs) == 0 {
		return 0, 0, nil
	}
	return len(pairs), s.markMembersStale(st), nil
}

func (s *Service) DeleteEnvVar(workspaceID, stackID uint, key string) (int, error) {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, stackID)
	if err != nil {
		return 0, ErrNotFound
	}
	if err := s.env.Delete(stackID, key); err != nil {
		return 0, err
	}
	return s.markMembersStale(st), nil
}

// IDByUID resolves a stack's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.repo.IDByUID(uid) }
