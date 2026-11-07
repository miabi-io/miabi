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

package models

import (
	"strings"
	"time"
)

type PlatformBackupSubject string

const (
	PlatformBackupDatabase PlatformBackupSubject = "database"
	PlatformBackupVolume   PlatformBackupSubject = "volume"
	// Encrypted under the backup passphrase; what makes a dump restorable onto a different host. See internal/dr.
	PlatformBackupIdentity PlatformBackupSubject = "identity"
	// Workload data that turns "control plane comes back" into "platform comes back"; needs IncludeTenantData.
	PlatformBackupTenantDatabase PlatformBackupSubject = "tenant-database"
	PlatformBackupTenantVolume   PlatformBackupSubject = "tenant-volume"
)

// PlatformBackup is one DR run of the platform itself, distinct from the per-workspace backup tables.
type PlatformBackup struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// Pointer because GORM creates a real FK and an ad-hoc backup belongs to no set; NULL means no set.
	SetID      *uint                 `json:"set_id,omitempty" gorm:"index"`
	Subject    PlatformBackupSubject `json:"subject" gorm:"not null"`
	VolumeName string                `json:"volume_name,omitempty"` // target volume (volume subjects)

	// Slug stored with the id because a restore reads a manifest where ids from a dead install mean nothing.
	WorkspaceID   uint   `json:"workspace_id,omitempty" gorm:"index"`
	WorkspaceSlug string `json:"workspace_slug,omitempty"`
	DatabaseName  string `json:"database_name,omitempty"`
	Engine        string `json:"engine,omitempty"`

	Status      BackupStatus `json:"status" gorm:"not null;default:pending"`
	Trigger     string       `json:"trigger"`     // manual | scheduled
	Destination string       `json:"destination"` // local | s3
	// What was actually written, not current settings, so a run predating the toggle still restores.
	Encrypted bool `json:"encrypted"`

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

// PlatformBackupSettings' S3 secret is encrypted at rest and never returned.
type PlatformBackupSettings struct {
	ID uint `json:"id" gorm:"primaryKey"`

	// S3 is the only destination: a backup on the host it protects is unreadable once that host is gone.
	S3Enabled        bool   `json:"s3_enabled"`
	S3Endpoint       string `json:"s3_endpoint,omitempty"`
	S3Bucket         string `json:"s3_bucket,omitempty"`
	S3Region         string `json:"s3_region,omitempty"`
	S3AccessKey      string `json:"s3_access_key,omitempty"`
	S3SecretKeyEnc   string `json:"-" gorm:"column:s3_secret_key_enc"` // crypto.Encrypt (platform scope)
	S3UseSSL         bool   `json:"s3_use_ssl"`
	S3ForcePathStyle bool   `json:"s3_force_path_style"`

	// RootPath lets one bucket hold several instances without colliding.
	RootPath           string `json:"root_path,omitempty"`
	DatabaseBackupPath string `json:"database_backup_path,omitempty"`
	VolumeBackupPath   string `json:"volume_backup_path,omitempty"`

	// Must never be MIABI_ENCRYPTION_KEY: a restore onto a fresh host cannot fetch a key that lived on the dead one.
	EncryptBackups bool `json:"encrypt_backups"`
	// Stored in the database being backed up, so useless for restore: shown once on save, record it out-of-band.
	BackupPassphraseEnc string `json:"-" gorm:"column:backup_passphrase_enc"`
	// Off only for operators who custody /etc/miabi/miabi.yaml themselves.
	IncludeIdentity bool `json:"include_identity"`

	// Off by default: it is the difference between megabytes and the size of the install.
	IncludeTenantData bool `json:"include_tenant_data"`

	ScheduleEnabled bool     `json:"schedule_enabled"`
	ScheduleCron    string   `json:"schedule_cron,omitempty"`
	MaxBackups      int      `json:"max_backups"`
	RetentionDays   int      `json:"retention_days"`
	Volumes         []string `json:"volumes" gorm:"serializer:json"` // platform volumes to include

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	S3SecretSet bool `json:"s3_secret_set" gorm:"-"`
	// PassphraseSet likewise reports presence of the backup passphrase only.
	PassphraseSet bool `json:"passphrase_set" gorm:"-"`
	// Env-supplied fields are read-only; the UI disables them rather than accept an edit that would be ignored.
	EnvLocked []string `json:"env_locked,omitempty" gorm:"-"`
}

const (
	DefaultDatabaseBackupPath = "databases"
	DefaultVolumeBackupPath   = "volumes"
)

// Normalize runs on read and write so rows saved before these fields existed get the same layout.
func (s *PlatformBackupSettings) Normalize() {
	root := strings.Trim(strings.TrimSpace(s.RootPath), "/")
	s.RootPath = root

	s.DatabaseBackupPath = joinPath(root, defaultIfBlank(s.DatabaseBackupPath, DefaultDatabaseBackupPath))
	s.VolumeBackupPath = joinPath(root, defaultIfBlank(s.VolumeBackupPath, DefaultVolumeBackupPath))
}

// Must be idempotent: Normalize runs on every read, and a growing "root/root/..." prefix would orphan artifacts.
func joinPath(root, path string) string {
	p := strings.Trim(strings.TrimSpace(path), "/")
	if root == "" || p == root || strings.HasPrefix(p, root+"/") {
		return p
	}
	return root + "/" + p
}

func defaultIfBlank(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
