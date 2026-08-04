// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wsbackup exports a workspace to a portable bundle on S3 and restores
// one back.
// It owns no storage machinery of its own. A database dump is services/backup's
// job, a volume archive is the volume-bkup helper's, and the bucket is the
// workspace's existing S3 target: what this package adds is the bundle — one
// self-describing tree of objects that a *different* install can read, indexed by
// an XML info file and anchored by a sealed state file (internal/wsbundle).
package wsbackup

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
	"github.com/miabi-io/miabi/internal/services/certificate"
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
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"github.com/miabi-io/miabi/internal/storage/blob"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

var (
	// ErrNotConfigured means the workspace cannot produce or read a bundle yet.
	// It wraps the missing piece (S3 target or passphrase) so the API can say
	// which one.
	ErrNotConfigured = errors.New("portable backup is not configured for this workspace")
	// ErrBusy means a run is already in flight for this workspace. Two concurrent
	// exports would race on nothing, but two concurrent restores race on every
	// resource they create — so both are serialized, per workspace.
	ErrBusy = errors.New("a portable backup run is already in progress for this workspace")
	// ErrNotFound means the bucket holds no bundle under that ref.
	ErrNotFound = errors.New("no bundle with that reference in the bucket")
)

// defaultS3Region is what the S3 client and the *-bkup helpers both fall back to
// when the workspace names no region. They must agree: a client and a helper
// signing for different regions is a harder failure to read than either alone.
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

// Enqueuer schedules a bundle run on the background worker. Satisfied by
// worker.Producer.
type Enqueuer interface {
	EnqueueWorkspaceBundle(bundleID uint) error
}

// Deps are the services a bundle run drives. They are the platform's own create
// paths, deliberately: a restore that wrote rows directly would skip quota,
// naming, Docker provisioning and every guard those paths own, and produce a
// workspace that looks right in the database and does not run.
type Deps struct {
	Repo       *repositories.WorkspaceBundleRepository
	Apps       *repositories.ApplicationRepository
	Users      *repositories.UserRepository
	Workspaces *repositories.WorkspaceRepository

	Settings    *backupsettings.Service
	Workspace   *workspace.Service
	App         *application.Service
	Volume      *storage.Service
	Database    *database.Service
	Secret      *secret.Service
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
	Environment *environment.Service
	Jobs        *job.Service
	Backup      *backup.Service
	Clients     NodeDocker
	Images      ImageResolver
	InstallID   string
	Version     string
}

// Service runs workspace bundle exports and restores.
type Service struct {
	Deps
	enqueuer Enqueuer
}

func NewService(d Deps) *Service { return &Service{Deps: d} }

// SetEnqueuer wires the background worker producer. Without one a run executes
// inline — correct for tests, and for a single-process deployment with no queue.
func (s *Service) SetEnqueuer(e Enqueuer) { s.enqueuer = e }

// Configured reports whether the workspace has everything a bundle needs. The UI
// asks before offering the action, so an operator is told what is missing instead
// of triggering a run that immediately fails.
func (s *Service) Configured(workspaceID uint) error {
	_, _, _, err := s.Settings.BundleTarget(workspaceID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	return nil
}

// store opens an S3 client for the workspace's bundle target.
func (s *Service) store(cfg *backup.S3Config) (*blob.Store, error) {
	return blob.New(blobConfig(cfg))
}

// blobConfig maps the workspace's backup target onto the object client's own
// config, defaulting the region to what the *-bkup helpers are given.
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

// Delete removes a run record. The bundle in the bucket is left alone — deleting
// a row must never be how data disappears; use DeleteBundle for that.
func (s *Service) Delete(workspaceID, id uint) error {
	b, err := s.Repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return err
	}
	return s.Repo.Delete(b.ID)
}

// Bundles lists the bundles present in the workspace's bucket, newest first,
// read from their info files. This is the authoritative list: the run records are
// this platform's memory of what it did, while the bucket is what actually
// survives — and a restore is offered from the bucket precisely because the
// platform that wrote it may be gone.
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
			// A bundle whose index does not parse is not a bundle this build can
			// restore; say so in the log rather than hiding it from the listing.
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

// DeleteBundle removes a bundle from the bucket: its info file, its state file
// and every artifact under its branch.
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
	// The branch first, the index last: an interrupted delete then leaves an info
	// file pointing at artifacts that are gone, which the restore reports as a
	// missing artifact — rather than artifacts no listing will ever show again.
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

// Run executes a pending run — the worker's entry point. A handled failure is
// recorded on the row and returns nil: a bundle run is not auto-retried, because
// a half-finished restore repeated blindly is worse than one an operator looks at.
func (s *Service) Run(ctx context.Context, id uint) error {
	b, err := s.Repo.FindByID(id)
	if err != nil {
		return fmt.Errorf("bundle run %d not found: %w", id, err)
	}
	if b.Status == models.BackupCompleted || b.Status == models.BackupFailed {
		return nil // already processed
	}
	now := time.Now()
	b.Status = models.BackupRunning
	b.StartedAt = &now
	_ = s.Repo.Update(b)

	switch b.Kind {
	case models.BundleRestore:
		return s.runRestore(ctx, b)
	default:
		return s.runExport(ctx, b)
	}
}

// phase records where a run is, so a long one says what it is doing.
func (s *Service) phase(b *models.WorkspaceBundle, phase string) {
	b.Phase = phase
	_ = s.Repo.Update(b)
}

// finish marks a run completed.
func (s *Service) finish(b *models.WorkspaceBundle) {
	fin := time.Now()
	b.Phase = models.BundlePhaseDone
	b.Status = models.BackupCompleted
	b.FinishedAt = &fin
	_ = s.Repo.Update(b)
	logger.Info("workspace bundle "+string(b.Kind)+" completed",
		"workspace", b.WorkspaceID, "ref", b.Ref, "artifacts", b.Artifacts)
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
