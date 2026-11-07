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

package repositories

import "github.com/miabi-io/miabi/internal/models"

func (r *ApplicationRepository) CountByWorkspace(workspaceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Application{}).Where("workspace_id = ?", workspaceID).Count(&n).Error
	return n, err
}

// CountWithoutLimitsByWorkspace counts apps missing a CPU or memory limit, which add nothing to compute totals.
func (r *ApplicationRepository) CountWithoutLimitsByWorkspace(workspaceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Application{}).
		Where("workspace_id = ? AND (memory_bytes = 0 OR nano_cpus = 0)", workspaceID).Count(&n).Error
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

// Ephemeral job/registry credentials are excluded so they don't consume the MaxAPIKeys quota.
func (r *APIKeyRepository) CountByWorkspace(workspaceID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.APIKey{}).
		Where("workspace_id = ? AND ephemeral = ?", workspaceID, false).Count(&n).Error
	return n, err
}

// Platform-shared runners and autoscaled ephemeral runners are never counted against the MaxRunners quota.
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

// SumResourcesByWorkspace counts every replica of a service app; excludeAppID = 0 excludes nothing.
func (r *ApplicationRepository) SumResourcesByWorkspace(workspaceID, excludeAppID uint) (nanoCPUs, memoryBytes int64, err error) {
	var row struct {
		CPU int64
		Mem int64
	}
	replicas := "CASE WHEN runtime_kind = 'service' AND replicas > 1 THEN replicas ELSE 1 END"
	q := r.db.Model(&models.Application{}).
		Select("COALESCE(SUM(nano_cpus * "+replicas+"),0) AS cpu, COALESCE(SUM(memory_bytes * "+replicas+"),0) AS mem").
		Where("workspace_id = ?", workspaceID)
	if excludeAppID > 0 {
		q = q.Where("id <> ?", excludeAppID)
	}
	if err = q.Scan(&row).Error; err != nil {
		return 0, 0, err
	}
	return row.CPU, row.Mem, nil
}

// Pass the app being redeployed as excludeAppID so its own GPU units aren't double-counted; 0 excludes none.
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

func (r *DatabaseRepository) SumVolumeSizeByWorkspace(workspaceID uint) (int64, error) {
	var total int64
	err := r.db.Model(&models.DatabaseInstance{}).
		Where("workspace_id = ?", workspaceID).
		Select("COALESCE(SUM(volume_size_bytes),0)").Scan(&total).Error
	return total, err
}

func (r *DatabaseRepository) SumResourcesByWorkspace(workspaceID, excludeID uint) (int64, int64, error) {
	var row struct {
		CPU    int64
		Memory int64
	}
	err := r.db.Model(&models.DatabaseInstance{}).
		Where("workspace_id = ? AND id <> ?", workspaceID, excludeID).
		Select("COALESCE(SUM(nano_cpus),0) AS cpu, COALESCE(SUM(memory_bytes),0) AS memory").Scan(&row).Error
	return row.CPU, row.Memory, err
}
