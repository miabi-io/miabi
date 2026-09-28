// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package locationmigration moves an application, with its volumes and databases, to another location while
// it keeps serving: the data is pre-copied live, the app stops only for a final delta and a deploy, and the
// source copy is kept until the move is finalized so it can be rolled back.
package locationmigration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/placement"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/internal/services/transfer"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrLocationRequired    = errors.New("choose the location to move the application to")
	ErrSameLocation        = errors.New("the application already runs in that location")
	ErrBlocked             = errors.New("the migration plan has blockers; resolve them first")
	ErrActive              = errors.New("this application is already being migrated")
	ErrNotFound            = errors.New("migration not found")
	ErrWrongState          = errors.New("the migration is not in a state that allows this")
	ErrPastPointOfNoReturn = errors.New("the application is already down for cutover; roll back instead of cancelling")
)

// DefaultGracePeriod is how long the source copy is kept after cutover, so the move can be rolled back.
const DefaultGracePeriod = 7 * 24 * time.Hour

// NodeDocker resolves a node's engine.
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

// Clusters is the Swarm side of a cluster. Satisfied by *cluster.Service.
type Clusters interface {
	IsSwarm(clusterID uint) bool
	Manager(ctx context.Context, clusterID uint) (docker.Client, error)
}

// Enqueuer schedules a migration run on the control-plane worker.
type Enqueuer interface {
	EnqueueLocationMigration(migrationID uint) error
}

// Deps are the services a migration drives. They are the platform's own paths, so a move gets the same
// guards and bookkeeping a create or a deploy does.
type Deps struct {
	Repo        *repositories.LocationMigrationRepository
	Apps        *repositories.ApplicationRepository
	VolumeRepo  *repositories.VolumeRepository
	ClusterRepo *repositories.ClusterRepository
	App         *application.Service
	Volumes     *storage.Service
	Databases   *database.Service
	Routes      *route.Service
	Placer      *placement.Service
	Clusters    Clusters
	Transfer    *transfer.Service
	Clients     NodeDocker
	Bus         *eventbus.Bus
}

// Service plans and runs location migrations.
type Service struct {
	Deps
	enqueuer Enqueuer
	grace    time.Duration
}

func NewService(d Deps) *Service { return &Service{Deps: d, grace: DefaultGracePeriod} }

// SetEnqueuer wires the worker. Without one a run executes inline, which is what tests do.
func (s *Service) SetEnqueuer(e Enqueuer) { s.enqueuer = e }

// StartRequest is a confirmed plan, sent back to start it.
type StartRequest struct {
	PlanRequest
	CutoverMode   string
	BandwidthKBps int
	StartedBy     *uint
}

// Start re-plans against the current state and, when nothing blocks, records the migration and queues it.
// Planning again is deliberate: the page may be minutes old, and a volume attached to another app since is
// exactly what must stop a move.
func (s *Service) Start(ctx context.Context, workspaceID, appID uint, req StartRequest) (*models.LocationMigration, error) {
	if m, err := s.Repo.ActiveForApp(appID); err == nil && m != nil {
		return nil, ErrActive
	}
	plan, app, err := s.plan(ctx, workspaceID, appID, req.PlanRequest)
	if err != nil {
		return nil, err
	}
	if len(plan.Blockers) > 0 {
		return nil, ErrBlocked
	}
	mode := req.CutoverMode
	if mode != models.CutoverManual {
		mode = models.CutoverAuto
	}
	bw := req.BandwidthKBps
	if bw < 0 {
		bw = 0
	}
	m := &models.LocationMigration{
		WorkspaceID:     workspaceID,
		ApplicationID:   app.ID,
		AppName:         app.Name,
		SourceClusterID: app.ClusterID,
		SourceServerID:  app.ServerID,
		TargetClusterID: plan.TargetClusterID,
		TargetServerID:  plan.TargetServerID,
		Status:          models.MigrationRunning,
		Phase:           models.MigrationPhasePrepare,
		CutoverMode:     mode,
		BandwidthKBps:   bw,
		Plan:            *plan,
		Progress:        initialProgress(plan),
		StartedByID:     req.StartedBy,
	}
	if err := s.Repo.Create(m); err != nil {
		return nil, err
	}
	s.publish(m)
	if err := s.enqueue(m.ID); err != nil {
		m.Status, m.Error = models.MigrationFailed, "could not queue the migration: "+err.Error()
		_ = s.Repo.Update(m)
		return nil, err
	}
	return m, nil
}

func (s *Service) enqueue(id uint) error {
	if s.enqueuer == nil {
		go func() { _ = s.Run(context.Background(), id) }()
		return nil
	}
	return s.enqueuer.EnqueueLocationMigration(id)
}

// Get returns a workspace's migration.
func (s *Service) Get(workspaceID, id uint) (*models.LocationMigration, error) {
	m, err := s.Repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return m, nil
}

// List returns a workspace's migrations, newest first.
func (s *Service) List(workspaceID uint) ([]models.LocationMigration, error) {
	return s.Repo.ListByWorkspace(workspaceID, 100)
}

// ListForApp returns an application's migrations, newest first.
func (s *Service) ListForApp(appID uint) ([]models.LocationMigration, error) {
	return s.Repo.ListByApp(appID, 20)
}

// ListAll returns every migration on the platform.
func (s *Service) ListAll() ([]models.LocationMigration, error) { return s.Repo.ListAll(200) }

// Cutover lets a manual migration waiting at the cutover point take the app down and finish.
func (s *Service) Cutover(workspaceID, id uint) (*models.LocationMigration, error) {
	m, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if m.Status != models.MigrationAwaitingCutover {
		return nil, ErrWrongState
	}
	if err := s.Repo.SetFlag(m.ID, "cutover_requested", true); err != nil {
		return nil, err
	}
	m.Status = models.MigrationRunning
	m.Phase = models.MigrationPhasePresync
	if err := s.Repo.Update(m); err != nil {
		return nil, err
	}
	m.CutoverRequested = true
	s.publish(m)
	return m, s.enqueue(m.ID)
}

// Cancel stops a migration before the app goes down. What the run created at the target is removed; the
// app never stopped, so nothing else changes.
func (s *Service) Cancel(workspaceID, id uint) (*models.LocationMigration, error) {
	m, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	switch m.Status {
	case models.MigrationAwaitingCutover:
		s.cancelNow(context.Background(), m)
		return m, nil
	case models.MigrationRunning:
		if m.State.AppStopped {
			return nil, ErrPastPointOfNoReturn
		}
		if err := s.Repo.SetFlag(m.ID, "cancel_requested", true); err != nil {
			return nil, err
		}
		m.CancelRequested = true
		return m, nil
	}
	return nil, ErrWrongState
}

// Rollback puts the app back where it was, on the data it left there. Writes made at the target since
// cutover are lost: this is a return to the pre-cutover state, not a migration back.
func (s *Service) Rollback(workspaceID, id uint) (*models.LocationMigration, error) {
	m, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if m.Status != models.MigrationCutOver && !(m.Status == models.MigrationFailed && m.State.AppStopped) {
		return nil, ErrWrongState
	}
	m.Status = models.MigrationRunning
	m.Phase = models.MigrationPhaseRollback
	m.Error = ""
	if err := s.Repo.Update(m); err != nil {
		return nil, err
	}
	s.publish(m)
	return m, s.enqueue(m.ID)
}

// Finalize deletes the source copy now instead of at the end of the grace period. After it, the move can
// no longer be rolled back.
func (s *Service) Finalize(workspaceID, id uint) (*models.LocationMigration, error) {
	m, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if m.Status != models.MigrationCutOver {
		return nil, ErrWrongState
	}
	m.Status = models.MigrationRunning
	m.Phase = models.MigrationPhaseFinalize
	if err := s.Repo.Update(m); err != nil {
		return nil, err
	}
	s.publish(m)
	return m, s.enqueue(m.ID)
}

// Sweep resumes runs whose worker died and finalizes migrations whose grace period ended. Called on a timer
// by the control plane.
func (s *Service) Sweep(ctx context.Context) {
	now := time.Now()
	if due, err := s.Repo.ListDueForFinalize(now); err == nil {
		for i := range due {
			m := &due[i]
			m.Status = models.MigrationRunning
			m.Phase = models.MigrationPhaseFinalize
			if err := s.Repo.Update(m); err != nil {
				continue
			}
			logger.Info("location migration: grace period over, removing the source copy", "migration", m.ID, "app", m.AppName)
			_ = s.enqueue(m.ID)
		}
	}
	if orphans, err := s.Repo.ListOrphaned(now); err == nil {
		for i := range orphans {
			// A just-created run has no lease until its worker picks it up; give the queue a minute.
			if now.Sub(orphans[i].UpdatedAt) < 2*time.Minute {
				continue
			}
			logger.Warn("location migration: resuming a run whose worker stopped", "migration", orphans[i].ID, "phase", orphans[i].Phase)
			_ = s.enqueue(orphans[i].ID)
		}
	}
}

// norm maps the default cluster's stand-in id 0 to its real id, so two ids compare.
func (s *Service) norm(clusterID uint) uint {
	if clusterID != models.DefaultClusterID || s.ClusterRepo == nil {
		return clusterID
	}
	if c, err := s.ClusterRepo.FindDefault(); err == nil {
		return c.ID
	}
	return clusterID
}

func (s *Service) clusterLabel(clusterID uint) string {
	if s.ClusterRepo == nil {
		return fmt.Sprint(clusterID)
	}
	c, err := s.ClusterRepo.FindByID(s.norm(clusterID))
	if err != nil {
		return fmt.Sprint(clusterID)
	}
	if c.DisplayName != "" {
		return c.DisplayName
	}
	return c.Name
}

func initialProgress(plan *models.MigrationPlan) models.MigrationProgress {
	var p models.MigrationProgress
	for _, v := range plan.Volumes {
		p.Items = append(p.Items, models.MigrationProgressItem{Kind: "volume", Name: v.Name, Status: "pending", Total: v.UsedBytes})
	}
	for _, d := range plan.Databases {
		p.Items = append(p.Items, models.MigrationProgressItem{Kind: "database", Name: d.InstanceName, Status: "pending", Detail: d.Strategy})
	}
	return p
}
