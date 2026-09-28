// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package locationmigration

import (
	"context"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
	"gorm.io/gorm"
)

// rollback puts the app back at the source on the data it left there, then removes what the move created
// at the target. cause is set when the rollback is automatic, after a failure past the switch.
func (s *Service) rollback(ctx context.Context, m *models.LocationMigration, cause error) {
	m.Phase = models.MigrationPhaseRollback
	m.Progress.Message = "Rolling back to " + s.clusterLabel(m.SourceClusterID)
	s.save(m)

	if m.State.Switched {
		if err := s.switchBack(ctx, m); err != nil {
			now := time.Now()
			m.Status = models.MigrationFailed
			m.Error = fmt.Sprintf("rollback failed: %v", err)
			if cause != nil {
				m.Error = fmt.Sprintf("%v; rollback failed: %v", cause, err)
			}
			m.FinishedAt = &now
			s.save(m)
			return
		}
	} else if m.State.AppStopped {
		s.restartSource(ctx, m)
	}
	s.cleanupTarget(ctx, m)

	now := time.Now()
	m.Status = models.MigrationRolledBack
	m.FinishedAt = &now
	m.FinalizeAfter = nil
	m.Progress.Message = "Serving at " + s.clusterLabel(m.SourceClusterID) + " again"
	if cause != nil {
		m.Error = cause.Error()
		m.Report.Note("The move failed after the switch and was rolled back automatically.")
	}
	s.save(m)
	logger.Info("location migration rolled back", "migration", m.ID, "app", m.AppName)
}

// switchBack reverses the switch: it takes the app down at the target, points every row back at the source,
// and starts the app and its instances there again.
func (s *Service) switchBack(ctx context.Context, m *models.LocationMigration) error {
	app, err := s.app(m)
	if err != nil {
		return err
	}
	targetGateway, hadGateway := s.Routes.GatewayServer(app)
	s.removeRuntime(ctx, app, m.TargetClusterID, m.TargetServerID, true)

	// The target containers of moved instances are recorded before the rows forget them.
	targetContainers := map[uint]string{}
	for _, ms := range m.State.Instances {
		if inst, err := s.Databases.FindInstance(ms.InstanceID); err == nil {
			targetContainers[ms.InstanceID] = inst.ContainerID
			_ = s.Databases.StopForMove(ctx, inst)
		}
	}

	err = s.Repo.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Application{}).Where("id = ?", m.ApplicationID).
			Updates(map[string]any{"server_id": m.SourceServerID, "cluster_id": m.SourceClusterID}).Error; err != nil {
			return err
		}
		for _, vs := range m.State.Volumes {
			if err := tx.Model(&models.Volume{}).Where("id = ?", vs.VolumeID).Updates(map[string]any{
				"server_id": vs.SourceServerID, "cluster_id": vs.SourceClusterID,
				"engine_created_at": vs.SourceCreatedAt, "mountpoint": vs.SourceMountpoint,
				"storage_class_name": vs.SourceStorageClass,
			}).Error; err != nil {
				return err
			}
		}
		for _, ms := range m.State.Instances {
			if err := tx.Model(&models.DatabaseInstance{}).Where("id = ?", ms.InstanceID).Updates(map[string]any{
				"server_id": ms.SourceServerID, "cluster_id": ms.SourceClusterID,
				"volume_engine_created_at": ms.SourceCreatedAt, "storage_class_name": ms.SourceStorageClass,
				"container_id": ms.SourceContainerID, "status": models.DBStatusStopped,
			}).Error; err != nil {
				return err
			}
		}
		for _, r := range m.State.Restores {
			if r.SourceDatabaseID == 0 || r.TargetDatabaseID == 0 {
				continue
			}
			if err := swapOwner(tx, r.TargetDatabaseID, r.SourceDatabaseID, m.ApplicationID, r.EnvPrefix); err != nil {
				return err
			}
		}
		m.State.Switched = false
		return tx.Omit("cutover_requested", "cancel_requested", "lease_until").Save(m).Error
	})
	if err != nil {
		m.State.Switched = true
		return err
	}

	for i := range m.State.Instances {
		m.State.Instances[i].TargetContainerID = targetContainers[m.State.Instances[i].InstanceID]
	}
	for _, ms := range m.State.Instances {
		inst, err := s.Databases.FindInstance(ms.InstanceID)
		if err != nil {
			return err
		}
		if err := s.Databases.StartStopped(ctx, inst); err != nil {
			return fmt.Errorf("start %s at the source: %w", inst.Name, err)
		}
	}
	app, err = s.app(m)
	if err != nil {
		return err
	}
	for _, r := range m.State.Restores {
		if r.SourceDatabaseID == 0 {
			continue
		}
		if err := s.inject(app, r.SourceInstanceID, r.SourceDatabaseID, r.EnvPrefix); err != nil {
			return err
		}
	}
	dep, err := s.App.RelocateDeploy(app, "migration-rollback")
	if err != nil {
		return fmt.Errorf("deploy at the source: %w", err)
	}
	if err := s.awaitDeployment(ctx, dep.ID); err != nil {
		return err
	}
	// The container the app left stopped at the source is superseded by the one just deployed.
	if m.State.SourceContainerID != "" {
		if dc, err := s.Clients.For(m.SourceServerID); err == nil {
			_ = dc.RemoveContainer(ctx, m.State.SourceContainerID, true)
		}
	}
	if err := s.Routes.SyncRoute(ctx, app.ID); err != nil {
		logger.Warn("location migration: route resync after rollback failed", "migration", m.ID, "error", err)
	}
	if _, err := s.Routes.RehostApp(ctx, app); err != nil {
		logger.Warn("location migration: generated URLs after rollback failed", "migration", m.ID, "error", err)
	}
	if hadGateway {
		s.Routes.ReloadGateway(ctx, targetGateway)
	}
	return nil
}

// restartSource starts the app and its moved instances again where they were, for a failure after the stop
// but before the switch: nothing was repointed, so the source is still the truth.
func (s *Service) restartSource(ctx context.Context, m *models.LocationMigration) {
	for _, ms := range m.State.Instances {
		if inst, err := s.Databases.FindInstance(ms.InstanceID); err == nil && inst.Status != models.DBStatusRunning {
			if err := s.Databases.StartStopped(ctx, inst); err != nil {
				logger.Error("location migration: restart source instance failed", "migration", m.ID, "instance", inst.Name, "error", err)
			}
		}
	}
	app, err := s.app(m)
	if err != nil {
		return
	}
	if _, err := s.App.Start(ctx, app); err != nil {
		logger.Error("location migration: restart source app failed", "migration", m.ID, "app", app.Name, "error", err)
		m.Report.Note("The application could not be started again at its original location: " + err.Error())
	}
}

// removeRuntime removes the app's container or service from one side of the move.
func (s *Service) removeRuntime(ctx context.Context, app *models.Application, clusterID, serverID uint, active bool) {
	if app.RuntimeKind == models.RuntimeService {
		if !s.Clusters.IsSwarm(clusterID) {
			return
		}
		if mgr, err := s.Clusters.Manager(ctx, clusterID); err == nil {
			_ = mgr.ServiceRemove(ctx, node.AppAlias(app))
		}
		return
	}
	if !active {
		return
	}
	rel, err := s.App.ActiveRelease(app.ID)
	if err != nil || rel.ContainerID == "" {
		return
	}
	if dc, err := s.Clients.For(serverID); err == nil {
		_ = dc.StopContainer(ctx, rel.ContainerID, 10)
		_ = dc.RemoveContainer(ctx, rel.ContainerID, true)
	}
}

// cleanupTarget removes everything the move created at the target. The rows point at the source when it
// runs, so nothing here touches data the app still uses.
func (s *Service) cleanupTarget(ctx context.Context, m *models.LocationMigration) {
	for _, vs := range m.State.Volumes {
		if !vs.Prepared {
			continue
		}
		if err := s.Volumes.DropCopy(ctx, m.TargetServerID, vs.DockerName, vs.TargetStorageClass); err != nil {
			m.Report.Add("volume", vs.DockerName, "failed", "the copy at the target could not be removed: "+err.Error())
		}
	}
	for _, ms := range m.State.Instances {
		if !ms.Prepared {
			continue
		}
		if err := s.Databases.DropCopy(ctx, m.TargetServerID, ms.TargetContainerID, ms.VolumeName, ms.TargetStorageClass); err != nil {
			m.Report.Add("database", ms.VolumeName, "failed", "the copy at the target could not be removed: "+err.Error())
		}
	}
	created := map[uint]bool{}
	for _, r := range m.State.Restores {
		if r.TargetDatabaseID != 0 {
			if err := s.Databases.DeleteDatabase(ctx, m.WorkspaceID, r.TargetDatabaseID); err != nil {
				m.Report.Add("database", r.Name, "failed", "the restored copy could not be removed: "+err.Error())
			}
		}
		if r.CreatedInstance && r.TargetInstanceID != 0 {
			created[r.TargetInstanceID] = true
		}
	}
	for id := range created {
		inst, err := s.Databases.FindInstance(id)
		if err != nil {
			continue
		}
		if inst.Status == models.DBStatusRunning {
			_ = s.Databases.Stop(ctx, inst)
		}
		inst.Metadata = models.SetOwner(inst.Metadata, models.OwnerUser, 0, "")
		if err := s.Databases.Delete(ctx, inst); err != nil {
			m.Report.Add("database", inst.Name, "failed", "the instance created for the move could not be removed: "+err.Error())
		}
	}
	m.State.Volumes, m.State.Instances, m.State.Restores = nil, nil, nil
}

// finalize deletes the source copy: the app's old container or service, the volumes and instances it left
// there, and the logical databases it was restored from. The move can no longer be rolled back.
func (s *Service) finalize(ctx context.Context, m *models.LocationMigration) {
	app, err := s.app(m)
	if err == nil {
		if app.RuntimeKind == models.RuntimeService {
			s.removeRuntime(ctx, app, m.SourceClusterID, m.SourceServerID, false)
		} else if m.State.SourceContainerID != "" {
			if dc, err := s.Clients.For(m.SourceServerID); err == nil {
				_ = dc.StopContainer(ctx, m.State.SourceContainerID, 10)
				_ = dc.RemoveContainer(ctx, m.State.SourceContainerID, true)
			}
		}
	}
	for _, vs := range m.State.Volumes {
		if err := s.Volumes.DropCopy(ctx, vs.SourceServerID, vs.DockerName, vs.SourceStorageClass); err != nil {
			m.Report.Add("volume", vs.DockerName, "failed", "the source copy could not be removed: "+err.Error())
		}
	}
	for _, ms := range m.State.Instances {
		if err := s.Databases.DropCopy(ctx, ms.SourceServerID, ms.SourceContainerID, ms.VolumeName, ms.SourceStorageClass); err != nil {
			m.Report.Add("database", ms.VolumeName, "failed", "the source copy could not be removed: "+err.Error())
		}
	}
	emptied := map[uint]bool{}
	for _, r := range m.State.Restores {
		if r.SourceDatabaseID == 0 {
			continue
		}
		if err := s.Databases.DeleteDatabase(ctx, m.WorkspaceID, r.SourceDatabaseID); err != nil {
			m.Report.Add("database", r.Name, "failed", "the source database could not be removed: "+err.Error())
			continue
		}
		emptied[r.SourceInstanceID] = true
	}
	// An instance the app owned and no longer uses at all goes too; a shared one is never touched.
	for id := range emptied {
		inst, err := s.Databases.FindInstance(id)
		if err != nil {
			continue
		}
		owner, ok := models.Owner(inst.Metadata)
		if !ok || owner.Kind != models.OwnerApp || owner.ID != m.ApplicationID {
			continue
		}
		if left, err := s.Databases.ListInstanceDatabases(id); err != nil || len(left) > 0 {
			continue
		}
		if inst.Status == models.DBStatusRunning {
			_ = s.Databases.Stop(ctx, inst)
		}
		inst.Metadata = models.SetOwner(inst.Metadata, models.OwnerUser, 0, "")
		if err := s.Databases.Delete(ctx, inst); err != nil {
			m.Report.Add("database", inst.Name, "failed", "the emptied source instance could not be removed: "+err.Error())
		}
	}
	now := time.Now()
	m.Status = models.MigrationFinalized
	m.Phase = models.MigrationPhaseDone
	m.FinishedAt = &now
	m.FinalizeAfter = nil
	m.Progress.Message = "Finalized: the source copy was removed"
	s.save(m)
	logger.Info("location migration finalized", "migration", m.ID, "app", m.AppName)
}
