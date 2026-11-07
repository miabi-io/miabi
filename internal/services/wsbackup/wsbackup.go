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

// Package wsbackup exports a workspace to a portable S3 bundle another install can read, and restores it.
package wsbackup

import (
	"context"
	"errors"
	"fmt"
	"github.com/miabi-io/miabi/internal/services/placement"
	"sort"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
	"github.com/miabi-io/miabi/internal/services/certificate"
	configsvc "github.com/miabi-io/miabi/internal/services/config"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/dnsprovider"
	"github.com/miabi-io/miabi/internal/services/domain"
	"github.com/miabi-io/miabi/internal/services/environment"
	"github.com/miabi-io/miabi/internal/services/gitops"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	"github.com/miabi-io/miabi/internal/services/job"
	"github.com/miabi-io/miabi/internal/services/middleware"
	"github.com/miabi-io/miabi/internal/services/network"
	"github.com/miabi-io/miabi/internal/services/pipeline"
	"github.com/miabi-io/miabi/internal/services/registry"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/services/sealing"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/internal/services/versioning"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"github.com/miabi-io/miabi/internal/storage/blob"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

var (
	// Wraps the missing piece (S3 target or passphrase) so the API can say which one.
	ErrNotConfigured = errors.New("portable backup is not configured for this workspace")
	// Concurrent restores race on every resource they create, so runs are serialized per workspace.
	ErrBusy = errors.New("a portable backup run is already in progress for this workspace")
	// ErrNotFound means the bucket holds no bundle under that ref.
	ErrNotFound = errors.New("no bundle with that reference in the bucket")
)

// The S3 client and *-bkup helpers must agree: signing for different regions is a hard failure to read.
const defaultS3Region = "us-east-1"

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

type Enqueuer interface {
	EnqueueWorkspaceBundle(bundleID uint) error
}

// The platform's own create paths: writing rows directly would skip quota, naming, provisioning and guards.
type Deps struct {
	Repo       *repositories.WorkspaceBundleRepository
	Apps       *repositories.ApplicationRepository
	Users      *repositories.UserRepository
	Workspaces *repositories.WorkspaceRepository

	Settings  *backupsettings.Service
	Workspace *workspace.Service
	App       *application.Service
	Volume    *storage.Service
	Database  *database.Service
	Secret    *secret.Service
	// Sealing carries the workspace's sealing keys; nil leaves them out of the bundle.
	Sealing     *sealing.Service
	Config      *configsvc.Service
	Route       *route.Service
	Middleware  *middleware.Service
	Domain      *domain.Service
	DNSProvider *dnsprovider.Service
	Certificate *certificate.Service
	Stack       *stack.Service
	Network     *network.Service
	Registry    *registry.Service
	GitRepo     *gitrepo.Service
	GitOps      *gitops.Service
	Pipeline    *pipeline.Service
	// Versions carries each pipeline's version policy; nil leaves policies out of the bundle.
	Versions    *versioning.Service
	Environment *environment.Service
	Jobs        *job.Service
	Backup      *backup.Service
	Clients     NodeDocker
	Images      ImageResolver
	// Nil places everything on the control-plane node, as before locations existed.
	Placer Placer
	// Freezer stops a migrating workspace's apps and refuses changes; nil disables the freeze option.
	Freezer Freezer
	// PublicURL is this install's address, named to a source when a migration retires it.
	PublicURL string
	InstallID string
	Version   string
}

// Placer resolves where a restore lands. Satisfied by the placement service.
type Placer interface {
	Place(req placement.Request) (placement.Result, error)
}

// Service runs workspace bundle exports and restores.
type Service struct {
	Deps
	enqueuer Enqueuer
}

func NewService(d Deps) *Service { return &Service{Deps: d} }

// Without one a run executes inline, for tests and single-process deployments with no queue.
func (s *Service) SetEnqueuer(e Enqueuer) { s.enqueuer = e }

func (s *Service) Configured(workspaceID uint) (encrypted bool, err error) {
	_, _, pass, err := s.Settings.BundleTarget(workspaceID)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	return pass != "", nil
}

func (s *Service) store(cfg *backup.S3Config) (*blob.Store, error) {
	return blob.New(blobConfig(cfg))
}

func blobConfig(cfg *backup.S3Config) blob.Config {
	region := cfg.Region
	if region == "" {
		region = defaultS3Region
	}
	return blob.Config{
		Endpoint:       cfg.Endpoint,
		Bucket:         cfg.Bucket,
		Region:         region,
		AccessKey:      cfg.AccessKey,
		SecretKey:      cfg.SecretKey,
		UseSSL:         cfg.UseSSL,
		ForcePathStyle: cfg.ForcePathStyle,
	}
}

// List returns the workspace's bundle runs, newest first.
func (s *Service) List(workspaceID uint, limit int) ([]models.WorkspaceBundle, error) {
	return s.Repo.ListByWorkspace(workspaceID, limit)
}

// Get returns one run, scoped to the workspace.
func (s *Service) Get(workspaceID, id uint) (*models.WorkspaceBundle, error) {
	return s.Repo.FindInWorkspace(workspaceID, id)
}

// Deleting a row must never be how data disappears; use DeleteBundle for that.
func (s *Service) Delete(workspaceID, id uint) error {
	b, err := s.Repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return err
	}
	return s.Repo.Delete(b.ID)
}

// The bucket, not the run records, is authoritative: it is what actually survives.
func (s *Service) Bundles(ctx context.Context, workspaceID uint) ([]wsbundle.Info, error) {
	cfg, prefix, _, err := s.Settings.BundleTarget(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	st, err := s.store(cfg)
	if err != nil {
		return nil, err
	}
	objects, err := st.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]wsbundle.Info, 0, len(objects))
	for _, o := range objects {
		if wsbundle.RefFromInfoObject(o.Key) == "" {
			continue // an artifact, not an index
		}
		body, err := st.GetBytes(ctx, o.Key)
		if err != nil {
			logger.Warn("bundle info could not be read", "key", o.Key, "error", err)
			continue
		}
		info, err := wsbundle.DecodeInfo(body)
		if err != nil {
			logger.Warn("bundle info could not be decoded", "key", o.Key, "error", err)
			continue
		}
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// FindBundle reads one bundle's info file from the bucket.
func (s *Service) FindBundle(ctx context.Context, workspaceID uint, ref string) (*wsbundle.Info, error) {
	if !wsbundle.IsRef(ref) {
		return nil, fmt.Errorf("%w: %q is not a bundle reference", ErrNotFound, ref)
	}
	cfg, prefix, _, err := s.Settings.BundleTarget(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	st, err := s.store(cfg)
	if err != nil {
		return nil, err
	}
	body, err := st.GetBytes(ctx, wsbundle.InfoObject(prefix, ref))
	if errors.Is(err, blob.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	if err != nil {
		return nil, err
	}
	return wsbundle.DecodeInfo(body)
}

func (s *Service) DeleteBundle(ctx context.Context, workspaceID uint, ref string) error {
	if !wsbundle.IsRef(ref) {
		return fmt.Errorf("%w: %q is not a bundle reference", ErrNotFound, ref)
	}
	cfg, prefix, _, err := s.Settings.BundleTarget(workspaceID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	st, err := s.store(cfg)
	if err != nil {
		return err
	}
	// Branch first, index last: an interrupted delete leaves a reportable missing artifact, not invisible orphans.
	objects, err := st.List(ctx, wsbundle.Root(prefix, ref))
	if err != nil {
		return err
	}
	for _, o := range objects {
		if err := st.Delete(ctx, o.Key); err != nil {
			return err
		}
	}
	return st.Delete(ctx, wsbundle.InfoObject(prefix, ref))
}

// start records a new run and schedules it, returning the pending row.
func (s *Service) start(ctx context.Context, b *models.WorkspaceBundle) (*models.WorkspaceBundle, error) {
	active, err := s.Repo.HasActive(b.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if active {
		return nil, ErrBusy
	}
	if err := s.Repo.Create(b); err != nil {
		return nil, err
	}
	if s.enqueuer == nil {
		_ = s.Run(ctx, b.ID)
		return s.Repo.FindByID(b.ID)
	}
	if err := s.enqueuer.EnqueueWorkspaceBundle(b.ID); err != nil {
		return s.fail(b, fmt.Errorf("enqueue bundle run: %w", err)), nil
	}
	return b, nil
}

// Not auto-retried: a half-finished restore repeated blindly is worse than one an operator looks at.
func (s *Service) Run(ctx context.Context, id uint) error {
	b, err := s.Repo.FindByID(id)
	if err != nil {
		return fmt.Errorf("bundle run %d not found: %w", id, err)
	}
	if b.Status == models.BackupCompleted || b.Status == models.BackupFailed || b.Status == models.BackupPartial {
		return nil // already processed
	}
	now := time.Now()
	b.Status = models.BackupRunning
	b.StartedAt = &now
	_ = s.Repo.Update(b)

	switch b.Kind {
	case models.BundleRestore:
		return s.runRestore(ctx, b)
	case models.BundleMigrationExport:
		return s.runMigrationExport(ctx, b)
	case models.BundleMigrationImport:
		return s.runMigrationImport(ctx, b)
	default:
		return s.runExport(ctx, b)
	}
}

// phase records where a run is, so a long one says what it is doing.
func (s *Service) phase(b *models.WorkspaceBundle, phase string) {
	b.Phase = phase
	_ = s.Repo.Update(b)
}

func (s *Service) finish(b *models.WorkspaceBundle) {
	fin := time.Now()
	b.Phase = models.BundlePhaseDone
	b.Status = models.BackupCompleted
	b.FinishedAt = &fin
	_ = s.Repo.Update(b)
	logger.Info("workspace bundle "+string(b.Kind)+" completed",
		"workspace", b.WorkspaceID, "ref", b.Ref, "artifacts", b.Artifacts)
}

func (s *Service) partial(b *models.WorkspaceBundle, cause error) {
	fin := time.Now()
	b.Phase = models.BundlePhaseDone
	b.Status = models.BackupPartial
	b.Error = cause.Error()
	b.FinishedAt = &fin
	_ = s.Repo.Update(b)
	logger.Warn("workspace bundle "+string(b.Kind)+" partially completed",
		"workspace", b.WorkspaceID, "ref", b.Ref, "error", cause)
}

// fail marks a run failed and returns it.
func (s *Service) fail(b *models.WorkspaceBundle, cause error) *models.WorkspaceBundle {
	fin := time.Now()
	b.Status = models.BackupFailed
	b.Error = cause.Error()
	b.FinishedAt = &fin
	_ = s.Repo.Update(b)
	logger.Error("workspace bundle "+string(b.Kind)+" failed",
		"workspace", b.WorkspaceID, "ref", b.Ref, "error", cause)
	return b
}
