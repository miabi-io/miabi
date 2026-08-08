// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package logstore

import "fmt"

// Object keys form one flat, predictable namespace so an operator can browse, ship or rotate logs
// by prefix; `ws_<id>` mirrors the registry's rename-proof workspace namespace. Keys are relative
// to the store root and carry NO leading "logs/"; old keys with that prefix still resolve.

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

// PlatformBackupRef is the key for a platform (disaster-recovery) backup run's
// output. Platform backups have no workspace — they are admin-only.
func PlatformBackupRef(backupID uint) string {
	return fmt.Sprintf("platform/pbackup-%d.log", backupID)
}
