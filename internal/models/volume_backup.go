// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// VolumeBackup is a single backup run of a managed volume's contents, archived
// (compressed) to the workspace's S3 target. Reuses BackupStatus.
//
// A row with a Ref is a recovery point (Enterprise): it lives under its own prefix beside a
// cleartext info.json, may be sealed with a per-point data key, is verified after each run and
// is subject to schedule retention. A row without one is a plain archive, as Community takes.
type VolumeBackup struct {
	ID          uint         `json:"id" gorm:"primaryKey"`
	WorkspaceID uint         `json:"workspace_id" gorm:"index;not null"`
	VolumeID    uint         `json:"volume_id" gorm:"index;not null"`
	ServerID    uint         `json:"server_id" gorm:"index;not null;default:0"` // node the volume lives on
	VolumeName  string       `json:"volume_name"`                               // docker volume name
	Status      BackupStatus `json:"status" gorm:"not null;default:pending"`
	Trigger     string       `json:"trigger"` // manual | scheduled

	// Ref is the stable, human-quotable name of a recovery point: "mbvol_<volume>_<UTC stamp>".
	// Empty on a plain archive; the partial index keeps those from colliding.
	Ref string `json:"ref,omitempty" gorm:"uniqueIndex:idx_volume_backups_ref,where:ref <> ''"`
	// ScheduleID names the schedule that took this point, so its retention runs after it lands.
	ScheduleID *uint `json:"schedule_id,omitempty" gorm:"index"`
	// Consistency records how the volume was read. "hot" archives while the workload runs,
	// which is crash-consistent only; restore UI says so rather than implying a point in time.
	Consistency string `json:"consistency,omitempty"`
	// Encrypted reports the archive is GPG-sealed, so restoring needs the workspace passphrase.
	Encrypted bool `json:"encrypted" gorm:"not null;default:false"`
	// Envelope seals this point's random data key under the workspace backup passphrase (see
	// internal/dbenvelope). Never serialized, as on DatabaseBackupSet.
	Envelope string `json:"-" gorm:"type:text"`
	// Pinned exempts the point from retention without occupying a retention slot.
	Pinned bool `json:"pinned" gorm:"not null;default:false"`

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

	// Verification: presence and size in the bucket, and that the envelope still opens.
	// An empty VerifyStatus means never checked.
	VerifiedAt   *time.Time `json:"verified_at,omitempty"`
	VerifyStatus string     `json:"verify_status,omitempty"` // ok | failed
	VerifyError  string     `json:"verify_error,omitempty" gorm:"type:text"`
}

// VolumeBackupRefPrefix marks a volume recovery point, as DatabaseBackupSetRefPrefix marks a
// database one.
const VolumeBackupRefPrefix = "mbvol_"

// VolumeConsistencyHot archives while the workload keeps running.
const VolumeConsistencyHot = "hot"

// NewVolumeBackupRef builds a point's stable name: "mbvol_<volume>_20260921T030000Z".
func NewVolumeBackupRef(volume string, at time.Time) string {
	if volume == "" {
		volume = "unknown"
	}
	return VolumeBackupRefPrefix + volume + "_" + at.UTC().Format("20060102T150405Z")
}

// IsPoint reports whether this row is a recovery point rather than a plain archive.
func (b *VolumeBackup) IsPoint() bool { return b.Ref != "" }

// VolumeBackupSchedule takes recovery points of one volume on a cron, then applies retention
// to what it produced. Retention lives here rather than on the point, as it does for
// DatabaseBackupSetSchedule.
type VolumeBackupSchedule struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index;not null"`
	VolumeID    uint   `json:"volume_id" gorm:"index;not null"`
	Cron        string `json:"cron" gorm:"not null"`
	Enabled     bool   `json:"enabled" gorm:"not null;default:true"`

	// MaxPoints keeps at most N points and RetentionDays drops any older than N days; 0 is
	// unbounded. Pinned points and the newest completed one always survive.
	MaxPoints     int `json:"max_points" gorm:"not null;default:0"`
	RetentionDays int `json:"retention_days" gorm:"not null;default:0"`

	LastRunAt *time.Time `json:"last_run_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}
