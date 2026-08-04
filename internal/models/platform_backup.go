// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// PlatformBackupSubject identifies what a platform backup run captured: Miabi's
// own control-plane database, or one of the platform/system Docker volumes.
type PlatformBackupSubject string

const (
	PlatformBackupDatabase PlatformBackupSubject = "database"
	PlatformBackupVolume   PlatformBackupSubject = "volume"
)

// PlatformBackup is a single disaster-recovery backup run of the platform itself
// — the control-plane database or a platform volume. It is distinct from the
// per-workspace Backup/VolumeBackup tables (tenant data) and reuses BackupStatus
// and the existing one-shot/logging lifecycle.
type PlatformBackup struct {
	ID          uint                  `json:"id" gorm:"primaryKey"`
	Subject     PlatformBackupSubject `json:"subject" gorm:"not null"` // database | volume
	VolumeName  string                `json:"volume_name,omitempty"`   // target volume (subject=volume)
	Status      BackupStatus          `json:"status" gorm:"not null;default:pending"`
	Trigger     string                `json:"trigger"`     // manual | scheduled
	Destination string                `json:"destination"` // local | s3

	S3Bucket  string `json:"s3_bucket,omitempty"`
	S3Path    string `json:"s3_path,omitempty"`  // remote folder prefix used
	Filename  string `json:"filename,omitempty"` // artifact object name
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

// PlatformBackupSettings is the single-row platform backup target + policy: the
// S3 destination (mirroring WorkspaceBackupSettings), the schedule, retention,
// and the set of platform volumes to include. The S3 secret is encrypted at rest
// with the platform-scoped crypto.Encrypt and never returned.
type PlatformBackupSettings struct {
	ID uint `json:"id" gorm:"primaryKey"`

	// Destination. S3 is strongly recommended for DR (a local artifact on the box
	// you are recovering from is no DR at all).
	S3Enabled        bool   `json:"s3_enabled"`
	S3Endpoint       string `json:"s3_endpoint,omitempty"`
	S3Bucket         string `json:"s3_bucket,omitempty"`
	S3Region         string `json:"s3_region,omitempty"`
	S3AccessKey      string `json:"s3_access_key,omitempty"`
	S3SecretKeyEnc   string `json:"-" gorm:"column:s3_secret_key_enc"` // crypto.Encrypt (platform scope)
	S3UseSSL         bool   `json:"s3_use_ssl"`
	S3ForcePathStyle bool   `json:"s3_force_path_style"`

	// Path prefixes within the bucket.
	DatabaseBackupPath string `json:"database_backup_path,omitempty"`
	VolumeBackupPath   string `json:"volume_backup_path,omitempty"`

	// Schedule + retention.
	ScheduleEnabled bool     `json:"schedule_enabled"`
	ScheduleCron    string   `json:"schedule_cron,omitempty"`
	MaxBackups      int      `json:"max_backups"`
	RetentionDays   int      `json:"retention_days"`
	Volumes         []string `json:"volumes" gorm:"serializer:json"` // platform volumes to include

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// S3SecretSet reports whether a secret key is stored, without exposing it.
	// Not persisted; populated on read so the UI can render "••••• (set)".
	S3SecretSet bool `json:"s3_secret_set" gorm:"-"`
}
