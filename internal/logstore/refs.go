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

package logstore

import "fmt"

// ws_<id> mirrors the registry's rename-proof namespace; old keys with a "logs/" prefix still resolve.

// DeploymentRef is the key for an application deployment's build/deploy log.
func DeploymentRef(workspaceID, appID, deploymentID uint) string {
	return fmt.Sprintf("deployment/ws_%d/app-%d/dep-%d.log", workspaceID, appID, deploymentID)
}

// PipelineStepRef is the key for one step within a pipeline run.
func PipelineStepRef(workspaceID, runID uint, ordinal int) string {
	return fmt.Sprintf("pipeline/ws_%d/run-%d/step-%d.log", workspaceID, runID, ordinal)
}

// PipelineRunRef is the key for a run's aggregate/checkout output.
func PipelineRunRef(workspaceID, runID uint) string {
	return fmt.Sprintf("pipeline/ws_%d/run-%d/run.log", workspaceID, runID)
}

// JobRef is the key for a one-off / cron job run's output.
func JobRef(workspaceID, jobID uint) string {
	return fmt.Sprintf("job/ws_%d/job-%d.log", workspaceID, jobID)
}

// BackupRef is the key for a managed-database backup run's output.
func BackupRef(workspaceID, backupID uint) string {
	return fmt.Sprintf("backup/ws_%d/backup-%d.log", workspaceID, backupID)
}

// VolumeBackupRef is the key for a managed-volume backup run's output.
func VolumeBackupRef(workspaceID, backupID uint) string {
	return fmt.Sprintf("volume-backup/ws_%d/vbackup-%d.log", workspaceID, backupID)
}

// Platform backups have no workspace: they are admin-only.
func PlatformBackupRef(backupID uint) string {
	return fmt.Sprintf("platform/pbackup-%d.log", backupID)
}
