// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// BackupStatus is the state of a backup run.
type BackupStatus string

const (
	BackupPending   BackupStatus = "pending"
	BackupRunning   BackupStatus = "running"
	BackupCompleted BackupStatus = "completed"
	BackupFailed    BackupStatus = "failed"
)

// Backup is a single backup run of a managed database.
type Backup struct {
	ID          uint         `json:"id" gorm:"primaryKey"`
	WorkspaceID uint         `json:"workspace_id" gorm:"index;not null"`
	DatabaseID  uint         `json:"database_id" gorm:"index;not null"`
	Engine      DBEngine     `json:"engine"`
	ServerID    uint         `json:"server_id" gorm:"index;not null;default:0"` // node the backup ran on
	Status      BackupStatus `json:"status" gorm:"not null;default:pending"`
	Trigger     string       `json:"trigger"`               // manual | scheduled
	Destination string       `json:"destination"`           // local | s3
	VolumeName  string       `json:"volume_name,omitempty"` // local destination
	S3Bucket    string       `json:"s3_bucket,omitempty"`   // s3 destination (reference)
	S3Path      string       `json:"s3_path,omitempty"`
	Filename    string       `json:"filename,omitempty"`
	SizeBytes   int64        `json:"size_bytes"`
	// Logs is a bounded tail of the backup output for instant display
	Logs         string     `json:"logs,omitempty" gorm:"type:text"`
	LogRef       string     `json:"log_ref,omitempty"`
	LogBytes     int64      `json:"log_bytes,omitempty"`
	LogLines     int        `json:"log_lines,omitempty"`
	LogTruncated bool       `json:"log_truncated,omitempty"`
	Error        string     `json:"error,omitempty" gorm:"type:text"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

// BackupSchedule runs backups of a database on a cron schedule. When
// Destination is "s3", the S3* fields hold the target (secret key encrypted).
type BackupSchedule struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index;not null"`
	DatabaseID  uint   `json:"database_id" gorm:"index;not null"`
	Cron        string `json:"cron" gorm:"not null"`
	Destination string `json:"destination" gorm:"not null;default:local"`
	Enabled     bool   `json:"enabled" gorm:"not null;default:true"`

	// Retention, applied after each scheduled run. 0 = unlimited.
	MaxBackups    int `json:"max_backups" gorm:"not null;default:0"`    // keep at most N most-recent backups
	RetentionDays int `json:"retention_days" gorm:"not null;default:0"` // delete backups older than N days

	S3Endpoint       string `json:"s3_endpoint,omitempty"`
	S3Bucket         string `json:"s3_bucket,omitempty"`
	S3Region         string `json:"s3_region,omitempty"`
	S3AccessKey      string `json:"s3_access_key,omitempty"`
	S3SecretKeyEnc   string `json:"-" gorm:"column:s3_secret_key_enc"` // encrypted
	S3Path           string `json:"s3_path,omitempty"`
	S3UseSSL         bool   `json:"s3_use_ssl"`
	S3ForcePathStyle bool   `json:"s3_force_path_style"`

	LastRunAt *time.Time `json:"last_run_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}
