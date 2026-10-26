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

// Package account orchestrates account-wide actions across a user's owned workspaces: stopping every
// app and database when an account is disabled, and cascade-deleting a user's data on removal. Each
// workspace has exactly one owner, so "a user's resources" are the resources in the workspaces they own.
package account

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// ErrSystemProtected is returned when a teardown targets the system workspace,
// which must never be deleted.
var ErrSystemProtected = errors.New("system workspace cannot be deleted")

// WorkspaceFinalizer performs the final workspace-row deletion and crypto-shred
// once its resources have been torn down. Satisfied by workspace.Service.
type WorkspaceFinalizer interface {
	Delete(id uint) error
}

// AppOps is the subset of the application service the teardown needs.
type AppOps interface {
	Stop(ctx context.Context, app *models.Application) error
	Delete(ctx context.Context, app *models.Application) error
}

// DBOps is the subset of the database service the teardown needs.
type DBOps interface {
	Stop(ctx context.Context, inst *models.DatabaseInstance) error
	Delete(ctx context.Context, inst *models.DatabaseInstance) error
	DetachFromApp(workspaceID, dbID uint) (*models.Database, error)
}

// StackOps is the subset of the stack service the teardown needs.
type StackOps interface {
	Delete(ctx context.Context, workspaceID, id uint, withApps bool) error
}

// StorageOps is the subset of the storage (volume) service the teardown needs.
type StorageOps interface {
	List(workspaceID uint) ([]models.Volume, error)
	Delete(ctx context.Context, v *models.Volume) error
}

// Service performs account-wide stop/teardown across a user's owned workspaces.
type Service struct {
	users      *repositories.UserRepository
	workspaces *repositories.WorkspaceRepository
	apps       *repositories.ApplicationRepository
	dbs        *repositories.DatabaseRepository
	stacks     *repositories.StackRepository
	appOps     AppOps
	dbOps      DBOps
	stackOps   StackOps
	storageOps StorageOps

	// finalizer removes the workspace row + crypto-shreds its keys after its
	// resources are torn down. Optional; falls back to a plain repo delete.
	finalizer WorkspaceFinalizer
	// deletionGuard refuses a user-initiated workspace delete (console-managed workspaces).
	deletionGuard func(ws *models.Workspace) error
	// onWorkspaceDeleted runs after a workspace row is gone, whatever started the delete.
	onWorkspaceDeleted func(ws *models.Workspace)
	// Live workspace-deletion jobs (single-node, in-process), streamed over the
	// event bus to the deletion-progress UI. bus may be nil in tests / unwired.
	bus       *eventbus.Bus
	delJobsMu sync.Mutex
	delJobs   map[string]*DeletionJob
}

// SetEventBus wires the bus used to stream deletion-job progress over SSE.
func (s *Service) SetEventBus(b *eventbus.Bus) { s.bus = b }

// SetWorkspaceFinalizer wires the component that deletes the workspace row and
// crypto-shreds its keys once teardown completes.
func (s *Service) SetWorkspaceFinalizer(f WorkspaceFinalizer) { s.finalizer = f }

// SetDeletionGuard wires the check GuardDeletion runs for deletes started from Miabi's own API.
func (s *Service) SetDeletionGuard(fn func(ws *models.Workspace) error) { s.deletionGuard = fn }

// SetWorkspaceDeletedHook wires a callback run after any workspace is deleted.
func (s *Service) SetWorkspaceDeletedHook(fn func(ws *models.Workspace)) { s.onWorkspaceDeleted = fn }

// GuardDeletion refuses a delete Miabi's own users may not make. The Billing API deletes through
// DeleteWorkspaceNow directly, so it is not subject to this.
func (s *Service) GuardDeletion(wsID uint) error {
	if s.deletionGuard == nil {
		return nil
	}
	ws, err := s.workspaces.FindByID(wsID)
	if err != nil {
		return nil
	}
	return s.deletionGuard(ws)
}

// NewService wires the teardown over the existing resource services and repos.
func NewService(
	users *repositories.UserRepository,
	workspaces *repositories.WorkspaceRepository,
	apps *repositories.ApplicationRepository,
	dbs *repositories.DatabaseRepository,
	stacks *repositories.StackRepository,
	appOps AppOps, dbOps DBOps, stackOps StackOps, storageOps StorageOps,
) *Service {
	return &Service{users: users, workspaces: workspaces, apps: apps, dbs: dbs, stacks: stacks, appOps: appOps, dbOps: dbOps, stackOps: stackOps, storageOps: storageOps}
}

// PurgeDue permanently deletes every account whose scheduled deletion time has
// passed: it tears down all of the account's owned-workspace data, then removes
// the user row. Returns the number of accounts purged. Best-effort per account.
func (s *Service) PurgeDue(ctx context.Context) int {
	if s.users == nil {
		return 0
	}
	due, err := s.users.ListDueForDeletion(time.Now())
	if err != nil {
		logger.Error("purge: list due accounts", "error", err)
		return 0
	}
	purged := 0
	for i := range due {
		u := due[i]
		res, err := s.PurgeAccount(ctx, u.ID)
		if err != nil {
			logger.Error("purge: delete user", "user", u.ID, "error", err)
			continue
		}
		logger.Info("purged account past deletion grace", "user", u.ID, "email", u.Email,
			"workspaces", res.Workspaces, "apps", res.Apps, "databases", res.Databases)
		purged++
	}
	return purged
}

// PurgeAccount permanently deletes a single account immediately, skipping any remaining grace period:
// it tears down all owned-workspace data (best-effort) then removes the user row. An error is returned
// only if the final user-row delete fails. Callers must enforce their own preconditions.
func (s *Service) PurgeAccount(ctx context.Context, userID uint) (DeleteResult, error) {
	if s.users == nil {
		return DeleteResult{}, errors.New("account service not configured")
	}
	res := s.DeleteOwned(ctx, userID)
	if err := s.users.Delete(userID); err != nil {
		return res, err
	}
	return res, nil
}

// StopResult counts what StopOwned acted on.
type StopResult struct {
	Apps      int `json:"apps"`
	Databases int `json:"databases"`
}

// StopOwned stops every running application and database instance in the
// workspaces the user owns. Best-effort: a single failure is logged and the
// sweep continues, so disabling an account never gets stuck on one bad container.
func (s *Service) StopOwned(ctx context.Context, userID uint) StopResult {
	var res StopResult
	wsIDs, err := s.workspaces.IDsOwnedBy(userID)
	if err != nil {
		logger.Error("account stop: list owned workspaces", "user", userID, "error", err)
		return res
	}
	for _, wsID := range wsIDs {
		r, _ := s.StopWorkspace(ctx, wsID)
		res.Apps += r.Apps
		res.Databases += r.Databases
	}
	return res
}

// StopWorkspace stops every running app and database instance of one workspace and returns what it
// stopped, so StartWorkspace can bring back exactly that.
func (s *Service) StopWorkspace(ctx context.Context, wsID uint) (StopResult, models.FrozenResources) {
	var res StopResult
	frozen := models.FrozenResources{Apps: []uint{}, Databases: []uint{}}
	apps, _ := s.apps.ListByWorkspace(wsID)
	for i := range apps {
		if apps[i].Status != models.AppStatusRunning {
			continue
		}
		if err := s.appOps.Stop(ctx, &apps[i]); err != nil {
			logger.Warn("workspace stop: stop app", "app", apps[i].ID, "error", err)
			continue
		}
		res.Apps++
		frozen.Apps = append(frozen.Apps, apps[i].ID)
	}
	insts, _ := s.dbs.ListByWorkspace(wsID)
	for i := range insts {
		if insts[i].Status != models.DBStatusRunning {
			continue
		}
		if err := s.dbOps.Stop(ctx, &insts[i]); err != nil {
			logger.Warn("workspace stop: stop database", "instance", insts[i].ID, "error", err)
			continue
		}
		res.Databases++
		frozen.Databases = append(frozen.Databases, insts[i].ID)
	}
	return res, frozen
}

// AppStarter and DBStarter start a stopped workload. Implemented by *application.Service and
// *database.Service; kept apart from AppOps/DBOps so account deletion does not depend on them.
type AppStarter interface {
	Start(ctx context.Context, app *models.Application) (*models.Deployment, error)
}

type DBStarter interface {
	Start(ctx context.Context, inst *models.DatabaseInstance) error
}

// StartResult counts what StartWorkspace brought back and what it could not.
type StartResult struct {
	Apps      int `json:"apps"`
	Databases int `json:"databases"`
	Failed    int `json:"failed"`
}

// StartWorkspace starts stopped workloads of a workspace, databases first so apps find them: those
// in only, or every stopped one when only is nil. Workloads deleted meanwhile, or no longer
// stopped, are skipped.
func (s *Service) StartWorkspace(ctx context.Context, wsID uint, only *models.FrozenResources) (StartResult, error) {
	var res StartResult
	appStarter, ok1 := s.appOps.(AppStarter)
	dbStarter, ok2 := s.dbOps.(DBStarter)
	if !ok1 || !ok2 {
		return res, errors.New("workspace start is not available")
	}
	want := func(ids []uint, id uint) bool {
		for _, x := range ids {
			if x == id {
				return true
			}
		}
		return false
	}
	insts, _ := s.dbs.ListByWorkspace(wsID)
	for i := range insts {
		if insts[i].Status != models.DBStatusStopped || (only != nil && !want(only.Databases, insts[i].ID)) {
			continue
		}
		if err := dbStarter.Start(ctx, &insts[i]); err != nil {
			logger.Warn("workspace start: start database", "instance", insts[i].ID, "error", err)
			res.Failed++
			continue
		}
		res.Databases++
	}
	apps, _ := s.apps.ListByWorkspace(wsID)
	for i := range apps {
		if apps[i].Status != models.AppStatusStopped || (only != nil && !want(only.Apps, apps[i].ID)) {
			continue
		}
		if _, err := appStarter.Start(ctx, &apps[i]); err != nil {
			logger.Warn("workspace start: start app", "app", apps[i].ID, "error", err)
			res.Failed++
			continue
		}
		res.Apps++
	}
	return res, nil
}

type DeleteResult struct {
	Workspaces int `json:"workspaces"`
	Apps       int `json:"apps"`
	Databases  int `json:"databases"`
	Stacks     int `json:"stacks"`
	Volumes    int `json:"volumes"`
}

// DeleteOwned permanently removes every resource in the user's owned workspaces, then the workspaces
// themselves. Best-effort and idempotent: failures are logged and the sweep continues. Apps go before
// databases so a database's owner and attachment guards no longer block it.
func (s *Service) DeleteOwned(ctx context.Context, userID uint) DeleteResult {
	var res DeleteResult
	workspaces, err := s.workspaces.ListOwnedBy(userID)
	if err != nil {
		logger.Error("account delete: list owned workspaces", "user", userID, "error", err)
		return res
	}
	for w := range workspaces {
		wsID := workspaces[w].ID
		sub := s.teardownResources(ctx, wsID, nil)
		res.Apps += sub.Apps
		res.Databases += sub.Databases
		res.Stacks += sub.Stacks
		res.Volumes += sub.Volumes

		if err := s.workspaces.Delete(wsID); err != nil {
			logger.Warn("account delete: delete workspace", "workspace", wsID, "error", err)
			continue
		}
		res.Workspaces++
	}
	return res
}

// teardownResources removes every application, database instance, stack and volume in one workspace, in the
// order that satisfies their cross-guards (apps before databases). Best-effort and idempotent: failures are
// logged and the sweep continues. A non-nil rep reports each phase for live progress; nil is a no-op.
func (s *Service) teardownResources(ctx context.Context, wsID uint, rep *delReporter) DeleteResult {
	var res DeleteResult

	// Applications first: Stop is best-effort (a stopped app has no container);
	// Delete tears down container, routes and host-port bindings.
	apps, _ := s.apps.ListByWorkspace(wsID)
	if len(apps) > 0 {
		rep.phase(PhaseApps, PhaseActive)
		for i := range apps {
			rep.note("Removing application " + apps[i].Name + "…")
			_ = s.appOps.Stop(ctx, &apps[i])
			if err := s.appOps.Delete(ctx, &apps[i]); err != nil {
				logger.Warn("workspace teardown: delete app", "app", apps[i].ID, "error", err)
				continue
			}
			res.Apps++
		}
		rep.phase(PhaseApps, PhaseDone)
	}

	// Database instances: detach logical databases (their app is gone now, but
	// the link still blocks instance deletion), stop, then delete (which removes
	// the container, data volume and scoped secrets).
	insts, _ := s.dbs.ListByWorkspace(wsID)
	if len(insts) > 0 {
		rep.phase(PhaseDatabases, PhaseActive)
		for i := range insts {
			rep.note("Removing database " + insts[i].Name + "…")
			if logical, lerr := s.dbs.ListDatabases(insts[i].ID); lerr == nil {
				for j := range logical {
					_, _ = s.dbOps.DetachFromApp(wsID, logical[j].ID)
				}
			}
			_ = s.dbOps.Stop(ctx, &insts[i])
			if err := s.dbOps.Delete(ctx, &insts[i]); err != nil {
				logger.Warn("workspace teardown: delete database", "instance", insts[i].ID, "error", err)
				continue
			}
			res.Databases++
		}
		rep.phase(PhaseDatabases, PhaseDone)
	}

	// Stacks (apps already removed, so a plain delete is enough).
	stacks, _ := s.stacks.ListByWorkspace(wsID)
	if len(stacks) > 0 {
		rep.phase(PhaseStacks, PhaseActive)
		for i := range stacks {
			rep.note("Removing stack " + stacks[i].Name + "…")
			if err := s.stackOps.Delete(ctx, wsID, stacks[i].ID, false); err != nil {
				logger.Warn("workspace teardown: delete stack", "stack", stacks[i].ID, "error", err)
				continue
			}
			res.Stacks++
		}
		rep.phase(PhaseStacks, PhaseDone)
	}

	vols, _ := s.storageOps.List(wsID)
	if len(vols) > 0 {
		rep.phase(PhaseVolumes, PhaseActive)
		for i := range vols {
			rep.note("Deleting volume " + vols[i].Name + "…")
			if err := s.storageOps.Delete(ctx, &vols[i]); err != nil {
				logger.Warn("workspace teardown: delete volume", "volume", vols[i].ID, "error", err)
				continue
			}
			res.Volumes++
		}
		rep.phase(PhaseVolumes, PhaseDone)
	}
	return res
}

// DeleteWorkspaceNow tears down a single workspace's resources and removes the workspace itself,
// synchronously. It is the non-streaming counterpart of the deletion-job path used by the UI, so CLI
// and API callers get the same full teardown. The system workspace is protected.
func (s *Service) DeleteWorkspaceNow(ctx context.Context, wsID uint) error {
	ws, err := s.workspaces.FindByID(wsID)
	if err != nil {
		return err
	}
	if ws.System {
		return ErrSystemProtected
	}
	s.teardownResources(ctx, wsID, nil)
	return s.finalizeWorkspace(wsID)
}

// finalizeWorkspace removes the workspace row and crypto-shreds its keys via the
// finalizer, falling back to a plain repo delete when none is wired (tests).
func (s *Service) finalizeWorkspace(wsID uint) error {
	ws, _ := s.workspaces.FindByID(wsID)
	var err error
	if s.finalizer != nil {
		err = s.finalizer.Delete(wsID)
	} else {
		err = s.workspaces.Delete(wsID)
	}
	if err == nil && ws != nil && s.onWorkspaceDeleted != nil {
		s.onWorkspaceDeleted(ws)
	}
	return err
}
