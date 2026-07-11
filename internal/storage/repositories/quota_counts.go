// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import "github.com/miabi-io/miabi/internal/models"

// Per-workspace resource counts used by the quota service to enforce plan
// limits. Each is a cheap COUNT(*) scoped to the workspace.

func (r *ApplicationRepository) CountByWorkspace(workspaceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Application{}).Where("workspace_id = ?", workspaceID).Count(&n).Error
	return n, err
}

func (r *VolumeRepository) CountByWorkspace(workspaceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Volume{}).Where("workspace_id = ?", workspaceID).Count(&n).Error
	return n, err
}

func (r *NetworkRepository) CountByWorkspace(workspaceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Network{}).Where("workspace_id = ?", workspaceID).Count(&n).Error
	return n, err
}

// CountInstancesByWorkspace counts database instances in a workspace.
func (r *DatabaseRepository) CountInstancesByWorkspace(workspaceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.DatabaseInstance{}).Where("workspace_id = ?", workspaceID).Count(&n).Error
	return n, err
}

// CountCronByWorkspace counts scheduled (cron) jobs in a workspace.
func (r *JobRepository) CountCronByWorkspace(workspaceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.CronJob{}).Where("workspace_id = ?", workspaceID).Count(&n).Error
	return n, err
}

// CountByWorkspace counts a workspace's user-managed API keys. Ephemeral,
// machine-minted job/registry credentials are excluded: they are per-run,
// short-lived, and must not consume the workspace's MaxAPIKeys quota.
func (r *APIKeyRepository) CountByWorkspace(workspaceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.APIKey{}).
		Where("workspace_id = ? AND ephemeral = ?", workspaceID, false).Count(&n).Error
	return n, err
}

// CountByWorkspace counts a workspace's owned (non-ephemeral) runners for the
// MaxRunners quota. Platform-shared runners (nil workspace) are never counted
// against a workspace, and autoscaled ephemeral runners are excluded.
func (r *RunnerRepository) CountByWorkspace(workspaceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Runner{}).
		Where("workspace_id = ? AND ephemeral = ?", workspaceID, false).Count(&n).Error
	return n, err
}

// CountDatabasesByInstance counts logical databases hosted on an instance.
func (r *DatabaseRepository) CountDatabasesByInstance(instanceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Database{}).Where("instance_id = ?", instanceID).Count(&n).Error
	return n, err
}

// SumResourcesByWorkspace returns the aggregate requested CPU (nanoCPUs) and
// memory (bytes) across a workspace's apps, optionally excluding one app (for
// update checks). excludeAppID = 0 excludes nothing.
func (r *ApplicationRepository) SumResourcesByWorkspace(workspaceID, excludeAppID uint) (nanoCPUs, memoryBytes int64, err error) {
	var row struct {
		CPU int64
		Mem int64
	}
	q := r.db.Model(&models.Application{}).
		Select("COALESCE(SUM(nano_cpus),0) AS cpu, COALESCE(SUM(memory_bytes),0) AS mem").
		Where("workspace_id = ?", workspaceID)
	if excludeAppID > 0 {
		q = q.Where("id <> ?", excludeAppID)
	}
	if err = q.Scan(&row).Error; err != nil {
		return 0, 0, err
	}
	return row.CPU, row.Mem, nil
}

// SumRunningGPUsByWorkspace returns the aggregate GPU units (GPUCount × replicas)
// held by a workspace's *running* apps — the live allocation the GPU quota is
// checked against. A stopped app holds nothing. excludeAppID = 0 excludes
// nothing; pass the app being (re)deployed so its own units aren't double-counted.
func (r *ApplicationRepository) SumRunningGPUsByWorkspace(workspaceID, excludeAppID uint) (int64, error) {
	var total int64
	q := r.db.Model(&models.Application{}).
		Select("COALESCE(SUM(gpu_count * CASE WHEN replicas < 1 THEN 1 ELSE replicas END),0)").
		Where("workspace_id = ? AND status = ? AND gpu_count > 0", workspaceID, models.AppStatusRunning)
	if excludeAppID > 0 {
		q = q.Where("id <> ?", excludeAppID)
	}
	err := q.Scan(&total).Error
	return total, err
}

// SumRunningGPUs returns the total GPU units held by every running app across the
// platform (for the miabi_gpu_allocated metric).
func (r *ApplicationRepository) SumRunningGPUs() (int64, error) {
	var total int64
	err := r.db.Model(&models.Application{}).
		Select("COALESCE(SUM(gpu_count * CASE WHEN replicas < 1 THEN 1 ELSE replicas END),0)").
		Where("status = ? AND gpu_count > 0", models.AppStatusRunning).Scan(&total).Error
	return total, err
}

// SumSizeByWorkspace returns the total declared volume size (bytes) in a workspace.
func (r *VolumeRepository) SumSizeByWorkspace(workspaceID uint) (int64, error) {
	var total int64
	err := r.db.Model(&models.Volume{}).
		Where("workspace_id = ?", workspaceID).
		Select("COALESCE(SUM(size_bytes),0)").Scan(&total).Error
	return total, err
}

// SumVolumeSizeByWorkspace returns the total declared DB-instance data-volume
// size (bytes) in a workspace.
func (r *DatabaseRepository) SumVolumeSizeByWorkspace(workspaceID uint) (int64, error) {
	var total int64
	err := r.db.Model(&models.DatabaseInstance{}).
		Where("workspace_id = ?", workspaceID).
		Select("COALESCE(SUM(volume_size_bytes),0)").Scan(&total).Error
	return total, err
}
