// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// VolumeBackup is a single backup run of a managed volume's contents, archived
// (compressed) to the workspace's S3 target. Reuses BackupStatus.
type VolumeBackup struct {
	ID          uint         `json:"id" gorm:"primaryKey"`
	WorkspaceID uint         `json:"workspace_id" gorm:"index;not null"`
	VolumeID    uint         `json:"volume_id" gorm:"index;not null"`
	ServerID    uint         `json:"server_id" gorm:"index;not null;default:0"` // node the volume lives on
	VolumeName  string       `json:"volume_name"`                               // docker volume name
	Status      BackupStatus `json:"status" gorm:"not null;default:pending"`
	Trigger     string       `json:"trigger"` // manual | scheduled

	S3Bucket  string `json:"s3_bucket,omitempty"`
	S3Path    string `json:"s3_path,omitempty"`  // remote folder prefix used
	Filename  string `json:"filename,omitempty"` // archive object name
	SizeBytes int64  `json:"size_bytes"`

	Logs         string `json:"logs,omitempty" gorm:"type:text"`
	LogRef       string `json:"log_ref,omitempty"`
	LogBytes     int64  `json:"log_bytes,omitempty"`
	LogLines     int    `json:"log_lines,omitempty"`
	LogTruncated bool   `json:"log_truncated,omitempty"`
	Error        string `json:"error,omitempty" gorm:"type:text"`

	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`
}
