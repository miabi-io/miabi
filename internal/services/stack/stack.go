// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package stack groups related applications in a workspace. A stack carries a user-facing name and a
// platform-managed Docker Compose project name, and member containers are labeled with it so Docker tooling
// groups them. Stacks are purely organizational — deleting one detaches its apps, which keep running.
package stack

import (
	"context"
	"errors"
	"fmt"
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
	ErrNameRequired    = errors.New("stack name is required")
	ErrNameTaken       = errors.New("a stack with this name already exists")
	ErrNotFound        = errors.New("stack not found")
	ErrAppNotFound     = errors.New("application not found in workspace")
	ErrAppInOtherStack = errors.New("application already belongs to another stack")
	ErrInvalidAction   = errors.New("invalid stack action")
	ErrKeyRequired     = errors.New("environment variable key is required")
)

// AppService is the slice of *application.Service the stack service drives:
// per-app container lifecycle, deploy, delete, create (for compose import), and
// env management. An interface keeps the stack service unit-testable with a fake.
type AppService interface {
	Start(ctx context.Context, app *models.Application) (*models.Deployment, error)
	Stop(ctx context.Context, app *models.Application) error
	Restart(ctx context.Context, app *models.Application) (*models.Deployment, error)
	WaitRunning(ctx context.Context, app *models.Application) error
	Deploy(app *models.Application, registryOverride *uint, tagOverride string, strategy models.DeployStrategy) (*models.Deployment, error)
	MarkRedeployRequired(app *models.Application) (bool, error)
	Delete(ctx context.Context, app *models.Application) error
	Create(workspaceID uint, in application.CreateInput) (*models.Application, error)
	SetEnvVar(appID uint, key, value string, isSecret bool) error
	AttachVolume(app *models.Application, volumeID uint, path string) error
}

// VolumeCreator provisions managed volumes, used by compose import to back the
// named volumes a compose file declares. Satisfied by *storage.Service.
type VolumeCreator interface {
	Create(ctx context.Context, workspaceID, serverID uint, name string, sizeBytes int64, meta, annotations models.Metadata) (*models.Volume, error)
}

// NetworkManager creates/removes the Docker network that backs a stack's
// service-name discovery. Satisfied by docker.Client.
type NetworkManager interface {
	EnsureNetwork(ctx context.Context, name string) (string, error)
	RemoveNetwork(ctx context.Context, name string) error
	// EnsureNetworkSpec / ListNetworks let the subnet allocator drive network
	// creation (satisfies netalloc.NetworkProvisioner).
	EnsureNetworkSpec(ctx context.Context, spec docker.NetworkSpec) (string, error)
	ListNetworks(ctx context.Context) ([]docker.Network, error)
}

// PortRequester files admin-validated host port-binding requests, used by
// compose import to capture published ports. Satisfied by *portbinding.Service.
type PortRequester interface {
	Request(workspaceID, userID uint, in portbinding.RequestInput) (*models.PortBinding, error)
	// RequestImport files a binding during import, tolerating host-port conflicts
	// (filed pending) and returning the conflicting owner instead of failing.
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

// SetAllocator wires the subnet allocator so a stack's network gets a Miabi-carved
// subnet instead of Docker's default pool (nil-safe; nil = Docker default pool).
func (s *Service) SetAllocator(a *netalloc.Service) { s.alloc = a }

type Input struct {
	// Name is the desired unique slug handle; it is normalized to canonical slug
	// form. DisplayName is the free-text label (falls back to Name when blank).
	Name        string
	DisplayName string
	Description string
	// Metadata is the stack's initial metadata. Callers may set reserved
	// "miabi.io/" keys (provenance); a missing managed-by defaults to "user".
	Metadata models.Metadata
	// Annotations is the stack's initial annotations: free-form descriptive notes
	// with no reserved keys (the manifest's metadata.annotations).
	Annotations models.Metadata
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
	// Per-stack network gives member apps service-name DNS, isolated from other
	// stacks. Derived from the (already unique) DockerName.
	dockerNetwork := "mb-stack-" + dockerName
	if s.docker != nil {
		spec := docker.NetworkSpec{Name: dockerNetwork}
		var err error
		if s.alloc != nil {
			// Carve a subnet from the Miabi pool so we don't exhaust Docker's small
			// built-in default-address-pools ("all predefined address pools have been
			// fully subnetted").
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
	}
	if err := s.repo.Create(st); err != nil {
		if s.docker != nil {
			_ = s.docker.RemoveNetwork(ctx, dockerNetwork) // roll back the docker side
		}
		return nil, err
	}
	return st, nil
}

// uniqueDockerName derives a Compose-safe project name (`ws{id}-{slug}`),
// suffixing on collision so it stays globally unique.
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
	Name        *string
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
			taken, err := s.repo.ExistsByName(workspaceID, name)
			if err != nil {
				return nil, err
			}
			if taken {
				return nil, ErrNameTaken
			}
			st.Name = name
		}
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

// Delete removes a stack. By default its applications are detached and keep
// running; when withApps is set, the member applications (and their containers)
// are deleted too.
func (s *Service) Delete(ctx context.Context, workspaceID, id uint, withApps bool) error {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	if withApps {
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
	// Best-effort: removing the network fails while detached apps still run on
	// it; it is reclaimed once those containers stop or redeploy elsewhere.
	if s.docker != nil && st.DockerNetwork != "" {
		_ = s.docker.RemoveNetwork(ctx, st.DockerNetwork)
		if s.alloc != nil {
			_ = s.alloc.Release(st.DockerNetwork) // return the subnet to the pool
		}
	}
	return nil
}

// AddApp assigns an application (in the same workspace) to a stack. An app
// already in a different stack is rejected rather than silently moved.
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

// RemoveApp clears an application's stack assignment (no-op if it is not a
// member of this stack).
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

// AppActionResult reports the outcome of a lifecycle action for one member app.
// Status is "ok" (acted), "skipped" (no active release to act on), or "failed".
type AppActionResult struct {
	AppID   uint   `json:"app_id"`
	AppName string `json:"app_name"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

// Lifecycle applies a start, stop or restart action to every application in the stack. Best-effort: an app
// with no active release is skipped and one that errors is recorded as failed, but neither aborts the others.
// When rolling is set, each app is waited until running before the next, so the stack is never fully down.
func (s *Service) Lifecycle(ctx context.Context, workspaceID, stackID uint, action Action, rolling bool) ([]AppActionResult, error) {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, stackID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.applyLifecycle(ctx, st.Apps, action, rolling)
}

// applyLifecycle runs the action over the given apps, classifying each result.
// Split out from Lifecycle so the per-app outcome logic is unit-testable
// without a database.
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

// DeployAll enqueues a deploy for every application in the stack, returning a
// per-app summary. Best-effort: one app's failure to enqueue doesn't stop the
// others.
func (s *Service) DeployAll(workspaceID, stackID uint) ([]DeployResult, error) {
	st, err := s.repo.FindInWorkspaceWithApps(workspaceID, stackID)
	if err != nil {
		return nil, ErrNotFound
	}
	results := make([]DeployResult, 0, len(st.Apps))
	for i := range st.Apps {
		app := &st.Apps[i]
		res := DeployResult{AppID: app.ID, AppName: app.Name, Status: "queued"}
		dep, derr := s.app.Deploy(app, nil, "", "")
		if derr != nil {
			res.Status, res.Error = "failed", derr.Error()
		} else {
			res.DeploymentID = dep.ID
		}
		results = append(results, res)
	}
	return results, nil
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

// ListEnvVars returns the stack's shared env vars (secret values masked).
func (s *Service) ListEnvVars(workspaceID, stackID uint) ([]models.StackEnvVar, error) {
	if _, err := s.repo.FindInWorkspace(workspaceID, stackID); err != nil {
		return nil, ErrNotFound
	}
	vars, err := s.env.ListByStack(stackID)
	if err != nil {
		return nil, err
	}
	for i := range vars {
		if vars[i].IsSecret {
			vars[i].Value = "••••••••"
		}
	}
	return vars, nil
}

// SetEnvVar upserts a shared env var on the stack (secret values encrypted).
func (s *Service) SetEnvVar(workspaceID, stackID uint, key, value string, isSecret bool) error {
	if _, err := s.repo.FindInWorkspace(workspaceID, stackID); err != nil {
		return ErrNotFound
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrKeyRequired
	}
	stored := value
	if isSecret {
		enc, err := crypto.EncryptWS(workspaceID, value)
		if err != nil {
			return err
		}
		stored = enc
	}
	return s.env.Upsert(&models.StackEnvVar{StackID: stackID, Key: key, Value: stored, IsSecret: isSecret})
}

// ImportEnvVars bulk-upserts shared env vars from .env-style content,
// encrypting them when isSecret. Returns the number set.
func (s *Service) ImportEnvVars(workspaceID, stackID uint, content string, isSecret bool) (int, error) {
	if _, err := s.repo.FindInWorkspace(workspaceID, stackID); err != nil {
		return 0, ErrNotFound
	}
	pairs := dotenv.Parse(content)
	for _, p := range pairs {
		stored := p.Value
		if isSecret {
			enc, err := crypto.EncryptWS(workspaceID, p.Value)
			if err != nil {
				return 0, err
			}
			stored = enc
		}
		if err := s.env.Upsert(&models.StackEnvVar{StackID: stackID, Key: p.Key, Value: stored, IsSecret: isSecret}); err != nil {
			return 0, err
		}
	}
	return len(pairs), nil
}

// DeleteEnvVar removes a shared env var from the stack.
func (s *Service) DeleteEnvVar(workspaceID, stackID uint, key string) error {
	if _, err := s.repo.FindInWorkspace(workspaceID, stackID); err != nil {
		return ErrNotFound
	}
	return s.env.Delete(stackID, key)
}

// IDByUID resolves a stack's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.repo.IDByUID(uid) }
