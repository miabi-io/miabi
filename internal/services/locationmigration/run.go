// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package locationmigration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/transfer"
	"gorm.io/gorm"
)

const (
	leaseFor   = 2 * time.Minute
	renewEvery = 30 * time.Second
	// maxPasses bounds the live pre-copy: a volume written faster than it can be copied never converges,
	// and the final pass has to take what is left.
	maxPasses = 5
	// smallDelta is where another live pass stops paying for itself.
	smallDelta = 64 << 20
	// deploySeconds is the part of the downtime estimate that is the deploy at the target.
	deploySeconds = 30
)

// errAwaiting ends a run that stops at the cutover point to wait for a person.
var errAwaiting = errors.New("awaiting cutover")

// errCancelled ends a run whose cancel was requested.
var errCancelled = errors.New("cancelled")

type step struct {
	phase string
	fn    func(context.Context, *models.LocationMigration) error
}

// Run executes a migration from the phase it recorded, so a run interrupted by a restart resumes. It is the
// worker entry point and always returns nil: failures are recorded on the row, never retried blindly.
func (s *Service) Run(ctx context.Context, id uint) error {
	m, err := s.Repo.FindByID(id)
	if err != nil || m.Status != models.MigrationRunning {
		return nil
	}
	now := time.Now()
	if ok, err := s.Repo.AcquireLease(id, now, now.Add(leaseFor)); err != nil || !ok {
		return nil
	}
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(renewEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_ = s.Repo.RenewLease(id, time.Now().Add(leaseFor))
			}
		}
	}()
	defer func() {
		close(stop)
		_ = s.Repo.ReleaseLease(id)
	}()
	defer func() {
		if r := recover(); r != nil {
			logger.Error("location migration panicked", "migration", id, "panic", r)
			s.fail(ctx, m, fmt.Errorf("internal error: %v", r))
		}
	}()

	switch m.Phase {
	case models.MigrationPhaseRollback:
		s.rollback(ctx, m, nil)
	case models.MigrationPhaseFinalize:
		s.finalize(ctx, m)
	default:
		s.forward(ctx, m)
	}
	return nil
}

func (s *Service) forward(ctx context.Context, m *models.LocationMigration) {
	steps := []step{
		{models.MigrationPhasePrepare, s.prepare},
		{models.MigrationPhasePresync, s.presync},
		{models.MigrationPhaseStop, s.stop},
		{models.MigrationPhaseFinalSync, s.finalSync},
		{models.MigrationPhaseSwitch, s.switchOver},
		{models.MigrationPhaseVerify, s.verify},
		{models.MigrationPhaseReroute, s.reroute},
	}
	start := 0
	for i, st := range steps {
		if st.phase == m.Phase {
			start = i
		}
	}
	for _, st := range steps[start:] {
		if s.cancelRequested(m) && !m.State.AppStopped {
			s.cancelNow(ctx, m)
			return
		}
		m.Phase = st.phase
		s.save(m)
		err := st.fn(ctx, m)
		switch {
		case err == nil:
			continue
		case errors.Is(err, errAwaiting):
			return
		case errors.Is(err, errCancelled):
			s.cancelNow(ctx, m)
			return
		}
		s.fail(ctx, m, fmt.Errorf("%s: %w", st.phase, err))
		return
	}
	now := time.Now()
	after := now.Add(s.grace)
	m.Status = models.MigrationCutOver
	m.Phase = models.MigrationPhaseDone
	m.CutoverAt = &now
	m.FinalizeAfter = &after
	m.Progress.Message = "Serving at " + m.Plan.LocationLabel
	s.save(m)
	logger.Info("location migration cut over", "migration", m.ID, "app", m.AppName, "to", m.Plan.Location)
}

// fail records a failure and undoes what it can. Before the app stopped nothing is lost, so the target is
// cleaned up. After, the app is brought back where it was: at the source it never left, or through a full
// rollback when placement had already switched.
func (s *Service) fail(ctx context.Context, m *models.LocationMigration, cause error) {
	logger.Error("location migration failed", "migration", m.ID, "app", m.AppName, "phase", m.Phase, "error", cause)
	switch {
	case m.State.Switched:
		s.rollback(ctx, m, cause)
		return
	case m.State.AppStopped:
		s.restartSource(ctx, m)
		s.cleanupTarget(ctx, m)
		m.Report.Note("The application was started again at its original location.")
	default:
		s.cleanupTarget(ctx, m)
	}
	now := time.Now()
	m.Status = models.MigrationFailed
	m.Error = cause.Error()
	m.FinishedAt = &now
	s.save(m)
}

func (s *Service) cancelNow(ctx context.Context, m *models.LocationMigration) {
	s.cleanupTarget(ctx, m)
	now := time.Now()
	m.Status = models.MigrationCancelled
	m.FinishedAt = &now
	m.Progress.Message = "Cancelled; the application kept running where it was"
	s.save(m)
}

func (s *Service) cancelRequested(m *models.LocationMigration) bool {
	_, cancel, err := s.Repo.Flags(m.ID)
	return err == nil && cancel
}

func (s *Service) save(m *models.LocationMigration) {
	if err := s.Repo.Update(m); err != nil {
		logger.Warn("location migration: persist progress failed", "migration", m.ID, "error", err)
	}
	s.publish(m)
}

func (s *Service) app(m *models.LocationMigration) (*models.Application, error) {
	return s.Apps.FindByID(m.ApplicationID)
}

// prepare creates everything the app needs at the target while it keeps serving: its volumes, the data
// volumes of the instances that move whole, and the instances databases are restored into.
func (s *Service) prepare(ctx context.Context, m *models.LocationMigration) error {
	app, err := s.app(m)
	if err != nil {
		return err
	}
	if m.State.SourceContainerID == "" && app.RuntimeKind != models.RuntimeService {
		if rel, err := s.App.ActiveRelease(app.ID); err == nil {
			m.State.SourceContainerID, m.State.SourceReleaseID = rel.ContainerID, rel.ID
		}
	}
	if id, ok := s.Routes.GatewayServer(app); ok {
		m.State.SourceGateway = id
	}
	target := m.TargetServerID

	for _, pv := range m.Plan.Volumes {
		if vs := volumeState(m, pv.VolumeID); vs != nil && vs.Prepared {
			continue
		}
		v, err := s.VolumeRepo.FindInWorkspace(m.WorkspaceID, pv.VolumeID)
		if err != nil {
			return fmt.Errorf("volume %s: %w", pv.Name, err)
		}
		t, err := s.Volumes.PrepareMove(ctx, v, target)
		if err != nil {
			return fmt.Errorf("create volume %s at the target: %w", v.Name, err)
		}
		m.State.Volumes = append(m.State.Volumes, models.MigrationVolumeState{
			VolumeID: v.ID, DockerName: v.DockerName,
			SourceServerID: v.ServerID, SourceClusterID: v.ClusterID, SourceCreatedAt: v.EngineCreatedAt,
			SourceMountpoint: v.Mountpoint, SourceStorageClass: v.StorageClassName,
			TargetCreatedAt: t.CreatedAt, TargetMountpoint: t.Mountpoint, TargetStorageClass: t.StorageClass,
			Prepared: true,
		})
		s.save(m)
	}

	for _, pd := range m.Plan.Databases {
		inst, err := s.Databases.FindInstance(pd.InstanceID)
		if err != nil {
			return fmt.Errorf("database %s: %w", pd.InstanceName, err)
		}
		switch pd.Strategy {
		case models.DBStrategyMove:
			if ms := moveState(m, inst.ID); ms != nil && ms.Prepared {
				continue
			}
			t, err := s.Databases.PrepareMove(ctx, inst, target)
			if err != nil {
				return fmt.Errorf("create the data volume of %s at the target: %w", inst.Name, err)
			}
			m.State.Instances = append(m.State.Instances, models.MigrationMoveState{
				InstanceID: inst.ID, VolumeName: inst.VolumeName,
				SourceServerID: inst.ServerID, SourceClusterID: inst.ClusterID, SourceContainerID: inst.ContainerID,
				SourceCreatedAt: inst.VolumeEngineCreatedAt, SourceStorageClass: inst.StorageClassName,
				SourceStatus:    string(inst.Status),
				TargetCreatedAt: t.CreatedAt, TargetStorageClass: t.StorageClass, Prepared: true,
			})
			s.save(m)
		case models.DBStrategyNewInstance, models.DBStrategyExistingInstance:
			if hasRestores(m, inst.ID) {
				continue
			}
			targetID, created := pd.TargetInstanceID, false
			if pd.Strategy == models.DBStrategyNewInstance {
				dst, err := s.provisionTarget(ctx, m, app, inst)
				if err != nil {
					return fmt.Errorf("provision a %s instance at the target: %w", inst.Engine, err)
				}
				targetID, created = dst.ID, true
			}
			for _, d := range pd.Databases {
				m.State.Restores = append(m.State.Restores, models.MigrationRestore{
					SourceDatabaseID: d.ID, SourceInstanceID: inst.ID, Name: d.Name, EnvPrefix: d.EnvPrefix,
					TargetInstanceID: targetID, CreatedInstance: created,
				})
			}
			s.save(m)
		}
	}
	s.prePull(ctx, app, target)
	return nil
}

// provisionTarget creates an instance at the target like the source one, owned by the app, and waits until
// it runs. Its logical databases are created later, at cutover, right before their data is loaded.
func (s *Service) provisionTarget(ctx context.Context, m *models.LocationMigration, app *models.Application, src *models.DatabaseInstance) (*models.DatabaseInstance, error) {
	meta := models.SetOwner(models.Metadata{}, models.OwnerApp, app.ID, app.Name)
	dst, err := s.Databases.Provision(ctx, m.WorkspaceID, m.TargetServerID, firstNonEmpty(src.DisplayName, src.Name),
		src.Engine, src.Version, src.VolumeSizeBytes, "",
		database.Resources{MemoryBytes: src.MemoryBytes, NanoCPUs: src.NanoCPUs, Size: src.SizeClass}, meta, nil)
	if err != nil {
		return nil, err
	}
	// Recorded before waiting, so a failure while it comes up still cleans it away.
	m.State.Restores = append(m.State.Restores, models.MigrationRestore{SourceInstanceID: src.ID, TargetInstanceID: dst.ID, CreatedInstance: true})
	s.save(m)
	deadline := time.Now().Add(15 * time.Minute)
	for {
		cur, err := s.Databases.FindInstance(dst.ID)
		if err == nil {
			switch cur.Status {
			case models.DBStatusRunning:
				m.State.Restores = m.State.Restores[:len(m.State.Restores)-1]
				return cur, nil
			case models.DBStatusFailed:
				return nil, fmt.Errorf("instance %s failed to start", cur.Name)
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("instance %s did not start in time", dst.Name)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// prePull fetches the app's image on the target ahead of the downtime. Best-effort: a private image without
// credentials here is simply pulled by the deploy.
func (s *Service) prePull(ctx context.Context, app *models.Application, serverID uint) {
	rel, err := s.App.ActiveRelease(app.ID)
	if err != nil || rel.Image == "" {
		return
	}
	if dc, err := s.Clients.For(serverID); err == nil {
		_ = dc.PullImage(ctx, rel.Image, nil)
	}
}

// copies are the volumes rsync moves: the app's own, and the data volumes of instances moving whole.
type copyJob struct {
	kind, name string
	src, dst   transfer.VolumeEnd
	key        string
}

func (s *Service) copyJobs(m *models.LocationMigration) []copyJob {
	var jobs []copyJob
	for _, pv := range m.Plan.Volumes {
		vs := volumeState(m, pv.VolumeID)
		if pv.Action != models.VolumeActionCopy || vs == nil {
			continue
		}
		jobs = append(jobs, copyJob{
			kind: "volume", name: pv.Name, key: fmt.Sprintf("%d-v%d", m.ID, pv.VolumeID),
			src: transfer.VolumeEnd{ServerID: vs.SourceServerID, Volume: vs.DockerName},
			dst: transfer.VolumeEnd{ServerID: m.TargetServerID, Volume: vs.DockerName},
		})
	}
	for _, ms := range m.State.Instances {
		name := fmt.Sprint(ms.InstanceID)
		for _, pd := range m.Plan.Databases {
			if pd.InstanceID == ms.InstanceID {
				name = pd.InstanceName
			}
		}
		jobs = append(jobs, copyJob{
			kind: "database", name: name, key: fmt.Sprintf("%d-d%d", m.ID, ms.InstanceID),
			src: transfer.VolumeEnd{ServerID: ms.SourceServerID, Volume: ms.VolumeName},
			dst: transfer.VolumeEnd{ServerID: m.TargetServerID, Volume: ms.VolumeName},
		})
	}
	return jobs
}

// presync copies the data while the app serves, pass after pass, until what is left to send stops
// shrinking. The last pass's delta is roughly what the final pass will face, which is the downtime.
func (s *Service) presync(ctx context.Context, m *models.LocationMigration) error {
	jobs := s.copyJobs(m)
	passes := maxPasses
	cutover, _, _ := s.Repo.Flags(m.ID)
	if cutover {
		// Resuming at a person's request: one catch-up pass, then the downtime.
		passes = 1
	}
	var prev int64 = -1
	for pass := 1; pass <= passes && len(jobs) > 0; pass++ {
		m.Progress.Pass = pass
		var delta, streamed int64
		var took time.Duration
		for _, j := range jobs {
			if s.cancelRequested(m) {
				return errCancelled
			}
			st, err := s.syncOne(ctx, m, j, false)
			if err != nil {
				return fmt.Errorf("copy %s %s: %w", j.kind, j.name, err)
			}
			delta += st.Transferred
			streamed += st.Streamed
			took += st.Duration
		}
		if took > 0 && streamed > 0 {
			m.Progress.BytesPerSecond = int64(float64(streamed) / took.Seconds())
		}
		m.Progress.LastDeltaBytes = delta
		m.Progress.EstimatedDowntimeSeconds = estimate(delta, m.Progress.BytesPerSecond)
		s.save(m)
		// The first pass carries the whole volume; stop once a pass sends little or no longer halves.
		if pass > 1 && (delta < smallDelta || (prev >= 0 && delta*2 > prev)) {
			break
		}
		prev = delta
	}
	if m.CutoverMode == models.CutoverManual && !cutover {
		m.Status = models.MigrationAwaitingCutover
		m.Progress.Message = "Data copied; waiting for the cutover"
		s.save(m)
		return errAwaiting
	}
	return nil
}

func estimate(delta, bps int64) int {
	if bps <= 0 {
		return deploySeconds
	}
	return int(delta/bps) + deploySeconds
}

func (s *Service) syncOne(ctx context.Context, m *models.LocationMigration, j copyJob, final bool) (transfer.SyncStats, error) {
	item := progressItem(m, j.kind, j.name)
	item.Status = "copying"
	s.save(m)
	st, err := s.Transfer.SyncVolume(ctx, j.src, j.dst, transfer.SyncOptions{
		Name: j.key, Final: final, BandwidthKBps: m.BandwidthKBps,
		Progress: func(n int64) {
			item.Bytes = n
			s.publish(m)
		},
	})
	if err != nil {
		item.Status, item.Detail = "failed", err.Error()
		s.save(m)
		return st, err
	}
	item.Passes++
	item.Bytes, item.Delta = st.Streamed, st.Transferred
	if st.Total > 0 {
		item.Total = st.Total
	}
	item.Status = "synced"
	if final {
		item.Status = "done"
	}
	s.save(m)
	return st, nil
}

// stop takes the app down at the source, and the instances that move whole with it. From here the move
// either completes or puts the app back.
func (s *Service) stop(ctx context.Context, m *models.LocationMigration) error {
	app, err := s.app(m)
	if err != nil {
		return err
	}
	now := time.Now()
	if m.DowntimeAt == nil {
		m.DowntimeAt = &now
	}
	m.State.AppStopped = true
	m.Progress.Message = "The application is stopped for the final copy"
	s.save(m)
	if app.Status != models.AppStatusStopped {
		if err := s.App.Stop(ctx, app); err != nil {
			return fmt.Errorf("stop the application: %w", err)
		}
	}
	for _, ms := range m.State.Instances {
		inst, err := s.Databases.FindInstance(ms.InstanceID)
		if err != nil {
			return err
		}
		if err := s.Databases.StopForMove(ctx, inst); err != nil {
			return fmt.Errorf("stop %s: %w", inst.Name, err)
		}
	}
	return nil
}

// finalSync copies what changed since the last live pass, with every writer stopped, and restores the
// databases that do not move whole. After it, the target holds exactly what the source did.
func (s *Service) finalSync(ctx context.Context, m *models.LocationMigration) error {
	for _, j := range s.copyJobs(m) {
		if _, err := s.syncOne(ctx, m, j, true); err != nil {
			return fmt.Errorf("final copy of %s %s: %w", j.kind, j.name, err)
		}
	}
	for i := range m.State.Restores {
		r := &m.State.Restores[i]
		if r.Restored || r.SourceDatabaseID == 0 {
			continue
		}
		if err := s.restoreOne(ctx, m, r); err != nil {
			item := progressItem(m, "database", r.Name)
			item.Status, item.Detail = "failed", err.Error()
			s.save(m)
			return fmt.Errorf("restore %s: %w", r.Name, err)
		}
	}
	return nil
}

func (s *Service) restoreOne(ctx context.Context, m *models.LocationMigration, r *models.MigrationRestore) error {
	src, err := s.Databases.FindInstance(r.SourceInstanceID)
	if err != nil {
		return err
	}
	srcDB, err := s.Databases.FindDatabase(r.SourceDatabaseID)
	if err != nil {
		return err
	}
	dst, err := s.Databases.FindInstance(r.TargetInstanceID)
	if err != nil {
		return err
	}
	var dstDB *models.Database
	if r.TargetDatabaseID == 0 {
		dstDB, err = s.Databases.CreateDatabase(ctx, m.WorkspaceID, dst.ID, r.Name, nil)
		if err != nil {
			return fmt.Errorf("create %s on %s: %w", r.Name, dst.Name, err)
		}
		r.TargetDatabaseID = dstDB.ID
		s.save(m)
	} else {
		if dstDB, err = s.Databases.FindDatabase(r.TargetDatabaseID); err != nil {
			return err
		}
		// A previous attempt may have loaded part of it.
		if err := s.Databases.RecreateDatabase(ctx, dst, dstDB); err != nil {
			return err
		}
	}
	dump, err := s.Databases.DumpSpec(ctx, src, srcDB, fmt.Sprintf("mb-mig-dump-%d-%d", m.ID, srcDB.ID))
	if err != nil {
		return err
	}
	load, err := s.Databases.RestoreSpec(ctx, dst, dstDB, fmt.Sprintf("mb-mig-load-%d-%d", m.ID, dstDB.ID))
	if err != nil {
		return err
	}
	item := progressItem(m, "database", r.Name)
	item.Status = "copying"
	s.save(m)
	n, err := s.Transfer.Stream(ctx,
		transfer.StreamEnd{ServerID: src.ServerID, Spec: dump},
		transfer.StreamEnd{ServerID: dst.ServerID, Spec: load},
		func(n int64) { item.Bytes = n; s.publish(m) })
	if err != nil {
		return err
	}
	item.Bytes, item.Status = n, "done"
	r.Restored = true
	s.save(m)
	return nil
}

// switchOver repoints the app, its volumes and the moved instances at the target in one transaction, then
// brings the instances up there and rewires the restored databases' connections. The source rows' former
// values stay in the migration state for a rollback.
func (s *Service) switchOver(ctx context.Context, m *models.LocationMigration) error {
	if !m.State.Switched {
		err := s.Repo.DB().Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.Application{}).Where("id = ?", m.ApplicationID).
				Updates(map[string]any{"server_id": m.TargetServerID, "cluster_id": m.TargetClusterID}).Error; err != nil {
				return err
			}
			for _, vs := range m.State.Volumes {
				if err := tx.Model(&models.Volume{}).Where("id = ?", vs.VolumeID).Updates(map[string]any{
					"server_id": m.TargetServerID, "cluster_id": m.TargetClusterID,
					"engine_created_at": vs.TargetCreatedAt, "mountpoint": vs.TargetMountpoint,
					"storage_class_name": vs.TargetStorageClass,
				}).Error; err != nil {
					return err
				}
			}
			for _, ms := range m.State.Instances {
				if err := tx.Model(&models.DatabaseInstance{}).Where("id = ?", ms.InstanceID).Updates(map[string]any{
					"server_id": m.TargetServerID, "cluster_id": m.TargetClusterID,
					"volume_engine_created_at": ms.TargetCreatedAt, "storage_class_name": ms.TargetStorageClass,
					"container_id": "", "status": models.DBStatusStopped,
				}).Error; err != nil {
					return err
				}
			}
			for _, r := range m.State.Restores {
				if r.SourceDatabaseID == 0 {
					continue
				}
				if err := swapOwner(tx, r.SourceDatabaseID, r.TargetDatabaseID, m.ApplicationID, r.EnvPrefix); err != nil {
					return err
				}
			}
			m.State.Switched = true
			return tx.Omit("cutover_requested", "cancel_requested", "lease_until").Save(m).Error
		})
		if err != nil {
			m.State.Switched = false
			return err
		}
		s.publish(m)
	}

	for _, ms := range m.State.Instances {
		inst, err := s.Databases.FindInstance(ms.InstanceID)
		if err != nil {
			return err
		}
		if inst.Status == models.DBStatusRunning && inst.ContainerID != "" {
			continue
		}
		if err := s.Databases.BringUpMoved(ctx, inst); err != nil {
			return fmt.Errorf("start %s at the target: %w", inst.Name, err)
		}
	}
	app, err := s.app(m)
	if err != nil {
		return err
	}
	for _, r := range m.State.Restores {
		if r.TargetDatabaseID == 0 {
			continue
		}
		if err := s.inject(app, r.TargetInstanceID, r.TargetDatabaseID, r.EnvPrefix); err != nil {
			return err
		}
	}
	return nil
}

// swapOwner hands an app's database from one logical database to another: from the source to its restored
// copy on a switch, and back on a rollback.
func swapOwner(tx *gorm.DB, fromID, toID, appID uint, prefix string) error {
	if err := tx.Model(&models.Database{}).Where("id = ?", fromID).
		Updates(map[string]any{"application_id": nil, "env_prefix": ""}).Error; err != nil {
		return err
	}
	return tx.Model(&models.Database{}).Where("id = ?", toID).
		Updates(map[string]any{"application_id": appID, "env_prefix": prefix}).Error
}

// verify deploys the running release at the target and waits for it to go live.
func (s *Service) verify(ctx context.Context, m *models.LocationMigration) error {
	if m.State.DeploymentID == 0 {
		app, err := s.app(m)
		if err != nil {
			return err
		}
		dep, err := s.App.RelocateDeploy(app, "migration")
		if err != nil {
			return fmt.Errorf("deploy at the target: %w", err)
		}
		m.State.DeploymentID = dep.ID
		m.Progress.Message = "Deploying at " + m.Plan.LocationLabel
		s.save(m)
	}
	return s.awaitDeployment(ctx, m.State.DeploymentID)
}

func (s *Service) awaitDeployment(ctx context.Context, id uint) error {
	deadline := time.Now().Add(20 * time.Minute)
	for {
		dep, err := s.App.GetDeployment(id)
		if err == nil && dep.Status.IsTerminal() {
			if dep.Status == models.DeploymentSucceeded {
				return nil
			}
			if dep.Error != "" {
				return fmt.Errorf("the deployment failed: %s", dep.Error)
			}
			return errors.New("the deployment failed")
		}
		if time.Now().After(deadline) {
			return errors.New("the deployment did not finish in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// reroute points the app's routes, DNS and generated URLs at the target's gateway, and tells the gateway it
// left to drop it.
func (s *Service) reroute(ctx context.Context, m *models.LocationMigration) error {
	app, err := s.app(m)
	if err != nil {
		return err
	}
	if err := s.Routes.SyncRoute(ctx, app.ID); err != nil {
		m.Report.Add("route", app.Name, "failed", err.Error())
	}
	urls, err := s.Routes.RehostApp(ctx, app)
	if err != nil {
		m.Report.Add("generated-url", app.Name, "failed", err.Error())
	}
	if m.State.SourceGateway != 0 {
		s.Routes.ReloadGateway(ctx, m.State.SourceGateway)
	}
	s.writeReport(m, app, urls)
	return nil
}

func (s *Service) writeReport(m *models.LocationMigration, app *models.Application, urls []string) {
	for _, v := range m.Plan.Volumes {
		m.Report.Add("volume", v.Name, "created", v.Action)
	}
	for _, d := range m.Plan.Databases {
		m.Report.Add("database", d.InstanceName, "created", d.Strategy)
	}
	for _, u := range urls {
		m.Report.Add("generated-url", u, "created", "")
	}
	if len(m.Plan.CustomDomains) > 0 {
		ip, host := s.Routes.DNSTarget(app)
		target := firstNonEmpty(host, ip)
		if target != "" {
			m.Report.Note(fmt.Sprintf("Domains whose DNS Miabi does not manage must now point at %s: %v. Records Miabi manages were updated.", target, m.Plan.CustomDomains))
		}
	}
	for _, w := range m.Plan.Warnings {
		if w.Code == "private_reach" {
			m.Report.Note(w.Message)
		}
	}
	m.Report.Note(fmt.Sprintf("The source copy is kept until %s so the move can be rolled back. Finalize to delete it sooner.",
		time.Now().Add(s.grace).Format("2006-01-02 15:04 MST")))
}

// inject writes a logical database's connection into the app's environment, the way attaching it does.
func (s *Service) inject(app *models.Application, instanceID, databaseID uint, prefix string) error {
	inst, err := s.Databases.FindInstance(instanceID)
	if err != nil {
		return err
	}
	db, err := s.Databases.FindDatabase(databaseID)
	if err != nil {
		return err
	}
	conn, err := s.Databases.DatabaseConnection(inst, db)
	if err != nil {
		return err
	}
	vars := []struct{ k, v string }{
		{"DATABASE_URL", "${{ secrets." + database.URLSecretName(inst, db) + " }}"},
		{"DB_HOST", conn.Host},
		{"DB_PORT", fmt.Sprint(conn.Port)},
		{"DB_NAME", conn.Database},
		{"DB_USER", conn.Username},
		{"DB_PASSWORD", "${{ secrets." + database.PasswordSecretName(inst, db) + " }}"},
	}
	for _, e := range vars {
		key := e.k
		if prefix != "" {
			key = prefix + "_" + e.k
		}
		if err := s.App.SetEnvVar(app.ID, key, e.v, false); err != nil {
			return err
		}
	}
	return nil
}

func volumeState(m *models.LocationMigration, id uint) *models.MigrationVolumeState {
	for i := range m.State.Volumes {
		if m.State.Volumes[i].VolumeID == id {
			return &m.State.Volumes[i]
		}
	}
	return nil
}

func moveState(m *models.LocationMigration, id uint) *models.MigrationMoveState {
	for i := range m.State.Instances {
		if m.State.Instances[i].InstanceID == id {
			return &m.State.Instances[i]
		}
	}
	return nil
}

func hasRestores(m *models.LocationMigration, sourceInstanceID uint) bool {
	for _, r := range m.State.Restores {
		if r.SourceInstanceID == sourceInstanceID && r.SourceDatabaseID != 0 {
			return true
		}
	}
	return false
}

func progressItem(m *models.LocationMigration, kind, name string) *models.MigrationProgressItem {
	for i := range m.Progress.Items {
		if m.Progress.Items[i].Kind == kind && m.Progress.Items[i].Name == name {
			return &m.Progress.Items[i]
		}
	}
	m.Progress.Items = append(m.Progress.Items, models.MigrationProgressItem{Kind: kind, Name: name, Status: "pending"})
	return &m.Progress.Items[len(m.Progress.Items)-1]
}
