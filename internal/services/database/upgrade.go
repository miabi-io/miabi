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

package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// AppController quiesces writers during a major (copy) upgrade; an interface avoids an app-service dependency.
type AppController interface {
	StopByID(ctx context.Context, workspaceID, appID uint) error
	StartByID(ctx context.Context, workspaceID, appID uint) error
}

type DumpRef struct {
	Filename string
	Volume   string
}

type LogicalBackup interface {
	Dump(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, comment string) (DumpRef, error)
	Load(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, ref DumpRef, force bool) error
}

func (s *Service) SetAppController(a AppController) { s.apps = a }

func (s *Service) SetLogicalBackup(b LogicalBackup) { s.backups = b }

// Persisted so both the worker (writer) and API server (reader) see live progress.
func (s *Service) setPhase(inst *models.DatabaseInstance, phase string) {
	if inst.Upgrade == nil {
		return
	}
	inst.Upgrade.Phase = phase
	if err := s.repo.Update(inst); err != nil {
		logger.Warn("persist upgrade phase", "id", inst.ID, "phase", phase, "error", err)
	}
	s.publishStatus(inst)
}

// UpgradeOptions describes what an instance can be upgraded to.
type UpgradeOptions struct {
	CurrentVersion string          `json:"current_version"`
	Engine         models.DBEngine `json:"engine"`
	Suggestions    []string        `json:"suggestions"`      // curated newer versions (hints; any version is accepted)
	AffectedAppIDs []uint          `json:"affected_app_ids"` // apps that would be stopped for a copy upgrade
}

// UpgradePlan is the resolved effect of upgrading to a specific target version.
type UpgradePlan struct {
	FromVersion    string `json:"from_version"`
	ToVersion      string `json:"to_version"`
	Path           string `json:"path"`  // in-place | dump-restore
	Major          bool   `json:"major"` // crosses a major version
	AffectedAppIDs []uint `json:"affected_app_ids"`
}

func (s *Service) affectedAppIDs(instanceID uint) []uint {
	dbs, err := s.repo.ListDatabases(instanceID)
	if err != nil {
		return nil
	}
	seen := map[uint]struct{}{}
	out := []uint{}
	for i := range dbs {
		if id := dbs[i].ApplicationID; id != nil && *id != 0 {
			if _, dup := seen[*id]; !dup {
				seen[*id] = struct{}{}
				out = append(out, *id)
			}
		}
	}
	return out
}

// UpgradeOptions returns the suggested targets and affected apps for an instance.
func (s *Service) UpgradeOptions(workspaceID, instanceID uint) (*UpgradeOptions, error) {
	inst, err := s.repo.FindInWorkspace(workspaceID, instanceID)
	if err != nil {
		return nil, ErrNotFound
	}
	return &UpgradeOptions{
		CurrentVersion: inst.Version,
		Engine:         inst.Engine,
		Suggestions:    suggestedVersions(inst.Engine, inst.Version),
		AffectedAppIDs: s.affectedAppIDs(instanceID),
	}, nil
}

func (s *Service) PlanUpgrade(workspaceID, instanceID uint, target string) (*UpgradePlan, error) {
	inst, err := s.repo.FindInWorkspace(workspaceID, instanceID)
	if err != nil {
		return nil, ErrNotFound
	}
	path, major, err := upgradePath(inst.Engine, inst.Version, target)
	if err != nil {
		return nil, err
	}
	return &UpgradePlan{
		FromVersion:    inst.Version,
		ToVersion:      strings.TrimSpace(target),
		Path:           path,
		Major:          major,
		AffectedAppIDs: s.affectedAppIDs(instanceID),
	}, nil
}

// Upgrade enqueues the work on asynq so it survives restarts; stopApps is strongly recommended for majors.
func (s *Service) Upgrade(ctx context.Context, workspaceID, instanceID uint, target string, stopApps bool) (*models.DatabaseInstance, error) {
	inst, err := s.repo.FindInWorkspace(workspaceID, instanceID)
	if err != nil {
		return nil, ErrNotFound
	}
	if inst.Status == models.DBStatusUpgrading {
		return nil, ErrUpgradeInProgress
	}
	if inst.Status != models.DBStatusRunning && inst.Status != models.DBStatusStopped {
		return nil, ErrInstanceNotUpgradable
	}
	path, major, err := upgradePath(inst.Engine, inst.Version, target)
	if err != nil {
		return nil, err
	}
	target = strings.TrimSpace(target)

	prevStatus := inst.Status
	inst.Status = models.DBStatusUpgrading
	inst.Upgrade = &models.UpgradeProgress{FromVersion: inst.Version, ToVersion: target, Path: path, Phase: "queued"}
	if err := s.repo.Update(inst); err != nil {
		return nil, err
	}
	if err := s.enqueuer.EnqueueUpgradeDB(instanceID, inst.ServerID, target, path, stopApps); err != nil {
		// Revert the optimistic state so the instance isn't stuck "upgrading".
		inst.Status, inst.Upgrade = prevStatus, nil
		_ = s.repo.Update(inst)
		s.publishStatus(inst)
		return nil, err
	}
	// Announce "upgrading/queued" immediately so an open stream reflects it.
	s.publishStatus(inst)
	logger.Info("database upgrade queued", "id", inst.ID, "from", inst.Version, "to", target, "path", path, "major", major)
	return s.Get(workspaceID, instanceID)
}

// RunUpgradeJob always returns nil: a half-applied upgrade must not be blindly retried.
func (s *Service) RunUpgradeJob(ctx context.Context, instanceID uint, target, path string, stopApps bool) error {
	inst, err := s.repo.FindByID(instanceID)
	if err != nil {
		return nil // instance gone; nothing to do
	}
	defer func() {
		if r := recover(); r != nil {
			logger.Error("database upgrade panicked", "id", instanceID, "panic", r)
			if fresh, ferr := s.repo.FindByID(instanceID); ferr == nil {
				fresh.Status = models.DBStatusFailed
				s.finishFailed(fresh, fmt.Errorf("internal error: %v", r))
			}
		}
	}()

	workspaceID := inst.WorkspaceID
	spec := specs[inst.Engine]
	affected := s.affectedAppIDs(instanceID)

	// The safety dumps double as the copy source for a major upgrade; Redis data rides on the volume.
	var dumps map[uint]DumpRef
	logicalDBs, _ := s.repo.ListDatabases(inst.ID)
	if s.backups != nil && inst.SupportsLogicalDatabases() && len(logicalDBs) > 0 {
		s.setPhase(inst, "backing-up")
		// The engine must be up to dump from; bring it up on its current volume.
		if err := s.ensureUp(ctx, inst, spec); err != nil {
			s.finishFailed(inst, fmt.Errorf("pre-upgrade start: %w", err))
			return nil
		}
		dumps = make(map[uint]DumpRef, len(logicalDBs))
		for i := range logicalDBs {
			// Names the upgrade it guarded, because that is the backup an operator looks for after a bad upgrade.
			ref, derr := s.backups.Dump(ctx, inst, &logicalDBs[i],
				fmt.Sprintf("Safety backup before the %s → %s upgrade", inst.Version, target))
			if derr != nil {
				s.finishFailed(inst, fmt.Errorf("backup %q: %w", logicalDBs[i].Name, derr))
				return nil
			}
			dumps[logicalDBs[i].ID] = ref
		}
	}

	// Quiesce writers before the data moves.
	if stopApps && s.apps != nil {
		s.setPhase(inst, "stopping-apps")
		for _, appID := range affected {
			if err := s.apps.StopByID(ctx, workspaceID, appID); err != nil {
				logger.Warn("stop app for db upgrade", "app", appID, "error", err)
			}
		}
	}

	switch path {
	case PathInPlace:
		err = s.upgradeInPlace(ctx, inst, spec, target)
	case PathDumpRestore:
		err = s.upgradeDumpRestore(ctx, inst, spec, target, logicalDBs, dumps)
	default:
		err = fmt.Errorf("unknown upgrade path %q", path)
	}
	// Restart writers regardless: they reconnect to the new engine, or to the rolled-back original.
	s.restartApps(ctx, workspaceID, affected, stopApps)
	if err != nil {
		s.finishFailed(inst, err)
		return nil
	}

	inst.Status = models.DBStatusRunning
	inst.Upgrade = nil
	if uerr := s.repo.Update(inst); uerr != nil {
		logger.Error("finalize db upgrade", "id", inst.ID, "error", uerr)
	}
	s.publishStatus(inst)
	s.emit(inst, models.EventDatabaseUpgraded, models.SeverityInfo,
		fmt.Sprintf("Upgraded to %s", target), map[string]string{"target": target, "path": path})
	logger.Info("database upgrade complete", "id", inst.ID, "version", target)
	return nil
}

// restartApps starts the given apps when they were auto-stopped.
func (s *Service) restartApps(ctx context.Context, workspaceID uint, appIDs []uint, wasStopped bool) {
	if !wasStopped || s.apps == nil {
		return
	}
	for _, appID := range appIDs {
		if err := s.apps.StartByID(ctx, workspaceID, appID); err != nil {
			logger.Warn("restart app after db upgrade", "app", appID, "error", err)
		}
	}
}

// Status is left as rollback set it: running on the original version, or failed if rollback couldn't restore it.
func (s *Service) finishFailed(inst *models.DatabaseInstance, cause error) {
	logger.Error("database upgrade failed", "id", inst.ID, "error", cause)
	if inst.Upgrade == nil {
		inst.Upgrade = &models.UpgradeProgress{}
	}
	inst.Upgrade.Phase = "failed"
	inst.Upgrade.Error = cause.Error()
	if err := s.repo.Update(inst); err != nil {
		logger.Error("persist upgrade failure", "id", inst.ID, "error", err)
	}
	s.publishStatus(inst)
	s.emit(inst, models.EventDatabaseUpgradeFailed, models.SeverityError, "Upgrade failed: "+cause.Error(), nil)
}

func (s *Service) ensureUp(ctx context.Context, inst *models.DatabaseInstance, spec engineSpec) error {
	if inst.ContainerID != "" {
		if dc, derr := s.dockerFor(inst); derr == nil {
			if c, ierr := dc.InspectContainer(ctx, inst.ContainerID); ierr == nil && c.State == "running" {
				return nil
			}
		}
	}
	adminPass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return err
	}
	if err := s.bringUp(ctx, inst, spec, adminPass); err != nil {
		return err
	}
	inst.Status = models.DBStatusUpgrading // bringUp set it to running; keep upgrading
	return s.repo.Update(inst)
}

// On failure it rolls back to the old image.
func (s *Service) upgradeInPlace(ctx context.Context, inst *models.DatabaseInstance, spec engineSpec, target string) error {
	s.setPhase(inst, "swapping")
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	adminPass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return err
	}
	oldVersion, oldImage := inst.Version, inst.Image
	if inst.ContainerID != "" {
		_ = dc.StopContainer(ctx, inst.ContainerID, 30)
		_ = dc.RemoveContainer(ctx, inst.ContainerID, true)
		inst.ContainerID = ""
	}
	newImage, _ := s.resolveEngineImage(spec, inst.Engine, target)
	inst.Version, inst.Image = target, newImage
	if err := s.repo.Update(inst); err != nil {
		return err
	}
	if err := s.bringUp(ctx, inst, spec, adminPass); err != nil {
		// Roll back to the previous image on the same (untouched) volume.
		logger.Warn("in-place upgrade failed; rolling back image", "id", inst.ID, "error", err)
		inst.Version, inst.Image, inst.ContainerID = oldVersion, oldImage, ""
		_ = s.repo.Update(inst)
		if rb := s.bringUp(ctx, inst, spec, adminPass); rb != nil {
			inst.Status = models.DBStatusFailed
			return fmt.Errorf("upgrade failed (%v) and rollback failed (%v)", err, rb)
		}
		return err
	}
	inst.Status = models.DBStatusUpgrading // re-assert; bringUp set running
	_ = s.repo.Update(inst)
	if inst.SupportsLogicalDatabases() {
		s.setPhase(inst, "verifying")
		if err := s.waitReady(ctx, inst); err != nil {
			return err
		}
	}
	return nil
}

// Preserves host alias, credentials and id so apps need no change; on failure restores the original volume.
func (s *Service) upgradeDumpRestore(ctx context.Context, inst *models.DatabaseInstance, spec engineSpec, target string, logicalDBs []models.Database, dumps map[uint]DumpRef) error {
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	adminPass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return err
	}
	oldVersion, oldImage, oldVolume := inst.Version, inst.Image, inst.VolumeName

	// Stop the old container (data preserved on oldVolume).
	s.setPhase(inst, "swapping")
	if inst.ContainerID != "" {
		_ = dc.StopContainer(ctx, inst.ContainerID, 30)
		_ = dc.RemoveContainer(ctx, inst.ContainerID, true)
		inst.ContainerID = ""
	}

	// Point the instance at a brand-new volume running the new engine version.
	newVolume := fmt.Sprintf("%s-v%s", oldVolume, sanitizeVolTag(target))
	newImage, _ := s.resolveEngineImage(spec, inst.Engine, target)
	inst.VolumeName, inst.Version, inst.Image = newVolume, target, newImage
	if err := s.repo.Update(inst); err != nil {
		return err
	}
	if err := s.bringUp(ctx, inst, spec, adminPass); err != nil {
		return s.rollbackVolume(ctx, dc, inst, spec, adminPass, oldVolume, oldVersion, oldImage, newVolume, err)
	}
	inst.Status = models.DBStatusUpgrading
	_ = s.repo.Update(inst)
	if err := s.waitReady(ctx, inst); err != nil {
		return s.rollbackVolume(ctx, dc, inst, spec, adminPass, oldVolume, oldVersion, oldImage, newVolume, err)
	}

	// Recreate each logical database + role on the fresh instance, then restore.
	s.setPhase(inst, "restoring")
	for i := range logicalDBs {
		d := &logicalDBs[i]
		if err := s.RecreateDatabase(ctx, inst, d); err != nil {
			return s.rollbackVolume(ctx, dc, inst, spec, adminPass, oldVolume, oldVersion, oldImage, newVolume, fmt.Errorf("recreate %q: %w", d.Name, err))
		}
		ref, ok := dumps[d.ID]
		if !ok {
			continue // nothing dumped (empty DB)
		}
		if err := s.backups.Load(ctx, inst, d, ref, false); err != nil {
			return s.rollbackVolume(ctx, dc, inst, spec, adminPass, oldVolume, oldVersion, oldImage, newVolume, fmt.Errorf("restore %q: %w", d.Name, err))
		}
	}

	// The safety dumps remain as the manual recovery floor.
	s.setPhase(inst, "verifying")
	if rerr := dc.RemoveVolume(context.Background(), oldVolume, true); rerr != nil {
		logger.Warn("remove old data volume after upgrade", "id", inst.ID, "volume", oldVolume, "error", rerr)
	}
	return nil
}

func (s *Service) rollbackVolume(ctx context.Context, dc docker.Client, inst *models.DatabaseInstance, spec engineSpec, adminPass, oldVolume, oldVersion, oldImage, newVolume string, cause error) error {
	logger.Warn("copy upgrade failed; rolling back to original volume", "id", inst.ID, "error", cause)
	if inst.ContainerID != "" {
		_ = dc.StopContainer(ctx, inst.ContainerID, 30)
		_ = dc.RemoveContainer(ctx, inst.ContainerID, true)
	}
	_ = dc.RemoveVolume(context.Background(), newVolume, true)
	inst.VolumeName, inst.Version, inst.Image, inst.ContainerID = oldVolume, oldVersion, oldImage, ""
	if err := s.repo.Update(inst); err != nil {
		inst.Status = models.DBStatusFailed
		return fmt.Errorf("upgrade failed (%v) and rollback persist failed (%v)", cause, err)
	}
	if rb := s.bringUp(ctx, inst, spec, adminPass); rb != nil {
		inst.Status = models.DBStatusFailed
		return fmt.Errorf("upgrade failed (%v) and rollback bring-up failed (%v)", cause, rb)
	}
	return cause
}

func sanitizeVolTag(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, v)
}
