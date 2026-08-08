// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"strings"
	"time"
)

// PlatformBackupSubject identifies what a platform backup run captured: Miabi's
// own control-plane database, or one of the platform/system Docker volumes.
type PlatformBackupSubject string

const (
	PlatformBackupDatabase PlatformBackupSubject = "database"
	PlatformBackupVolume   PlatformBackupSubject = "volume"
	// PlatformBackupIdentity is the sealed identity envelope: the platform's encryption key, JWT
	// secret and install identity, encrypted under the backup passphrase. It is what makes a dump
	// restorable onto a *different* host — see internal/dr.
	PlatformBackupIdentity PlatformBackupSubject = "identity"
	// PlatformBackupTenantDatabase and PlatformBackupTenantVolume are workload data: a
	// workspace's managed database or volume. They turn a recovery point from "the control plane
	// comes back" into "the platform comes back", and need IncludeTenantData.
	PlatformBackupTenantDatabase PlatformBackupSubject = "tenant-database"
	PlatformBackupTenantVolume   PlatformBackupSubject = "tenant-volume"
)

// PlatformBackup is a single disaster-recovery run of the platform itself — the control-plane
// database or a platform volume. Distinct from the per-workspace Backup/VolumeBackup tables,
// and reuses BackupStatus and the one-shot/logging lifecycle.
type PlatformBackup struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// SetID is the owning recovery point. A POINTER, not a plain uint: the association makes GORM
	// create a real foreign key, and an ad-hoc single-subject backup belongs to no set. Stored as
	// 0 it would reference a row that cannot exist, so NULL is what "no set" means.
	SetID      *uint                 `json:"set_id,omitempty" gorm:"index"`
	Subject    PlatformBackupSubject `json:"subject" gorm:"not null"`
	VolumeName string                `json:"volume_name,omitempty"` // target volume (volume subjects)

	// Tenant provenance, set on tenant-database and tenant-volume items. The slug
	// is stored alongside the id because a restore reads these from a recovery
	// point manifest, where a numeric id from a dead install means nothing.
	WorkspaceID   uint   `json:"workspace_id,omitempty" gorm:"index"`
	WorkspaceSlug string `json:"workspace_slug,omitempty"`
	DatabaseName  string `json:"database_name,omitempty"`
	Engine        string `json:"engine,omitempty"`

	Status      BackupStatus `json:"status" gorm:"not null;default:pending"`
	Trigger     string       `json:"trigger"`     // manual | scheduled
	Destination string       `json:"destination"` // local | s3
	// Encrypted records whether this artifact was actually written GPG-encrypted,
	// as opposed to what the settings say now — a run that predates the toggle
	// must still restore correctly.
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

// PlatformBackupSettings is the single-row platform backup target and policy: S3 destination,
// schedule, retention, and which platform volumes to include. The S3 secret is encrypted at
// rest with the platform-scoped crypto.Encrypt and never returned.
type PlatformBackupSettings struct {
	ID uint `json:"id" gorm:"primaryKey"`

	// Destination. S3/MinIO is the ONLY destination: a backup written to a volume on the host it
	// protects cannot be read once that host is gone, which is the one situation this exists for.
	// S3Enabled switches the feature off, it does not select an alternative.
	S3Enabled        bool   `json:"s3_enabled"`
	S3Endpoint       string `json:"s3_endpoint,omitempty"`
	S3Bucket         string `json:"s3_bucket,omitempty"`
	S3Region         string `json:"s3_region,omitempty"`
	S3AccessKey      string `json:"s3_access_key,omitempty"`
	S3SecretKeyEnc   string `json:"-" gorm:"column:s3_secret_key_enc"` // crypto.Encrypt (platform scope)
	S3UseSSL         bool   `json:"s3_use_ssl"`
	S3ForcePathStyle bool   `json:"s3_force_path_style"`

	// Path prefixes within the bucket. RootPath scopes the whole tree so one bucket can hold
	// several instances without colliding, and the recovery-point info file sits at its root.
	// Database and volume artifacts default to <root>/databases and <root>/volumes.
	RootPath           string `json:"root_path,omitempty"`
	DatabaseBackupPath string `json:"database_backup_path,omitempty"`
	VolumeBackupPath   string `json:"volume_backup_path,omitempty"`

	// Artifact encryption. EncryptBackups GPG-encrypts the dump and volume archives using
	// BackupPassphrase, which is NOT the master key (MIABI_ENCRYPTION_KEY) and must never be set
	// to it: a restore onto a fresh host cannot fetch a key that lived only on the dead one.
	EncryptBackups bool `json:"encrypt_backups"`
	// BackupPassphraseEnc is the passphrase at rest, encrypted under the master key — in the
	// database this feature backs up. Fine for taking backups and useless for restoring them, so
	// it is shown once on save and must be recorded out-of-band.
	BackupPassphraseEnc string `json:"-" gorm:"column:backup_passphrase_enc"`
	// IncludeIdentity seals the platform's identity (encryption key, JWT secret, install ID,
	// hostnames) into each recovery point so it can be restored onto a fresh host. Off only for
	// operators who custody /etc/miabi/stack.yaml themselves.
	IncludeIdentity bool `json:"include_identity"`

	// IncludeTenantData adds every workspace's managed databases and volumes, so a restore brings
	// back tenant workloads and not just the control plane describing them. Off by default: it is
	// the difference between megabytes and the size of the install.
	IncludeTenantData bool `json:"include_tenant_data"`

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
	// PassphraseSet likewise reports presence of the backup passphrase only.
	PassphraseSet bool `json:"passphrase_set" gorm:"-"`
	// EnvLocked names the fields supplied by environment variables on this
	// deployment. Those are read-only: the process configuration wins, and the UI
	// disables them rather than accepting an edit that would be silently ignored.
	EnvLocked []string `json:"env_locked,omitempty" gorm:"-"`
}

// Platform backup object-path defaults. An operator who does not care where the
// artifacts land should still get a tidy, predictable bucket layout rather than
// everything piled at the root.
const (
	DefaultDatabaseBackupPath = "databases"
	DefaultVolumeBackupPath   = "volumes"
)

// Normalize fills the derived defaults (database/volume prefixes, info-file format). Applied
// on both read and write, so a settings row saved before these fields existed behaves like a
// new one instead of scattering artifacts across the bucket root.
func (s *PlatformBackupSettings) Normalize() {
	root := strings.Trim(strings.TrimSpace(s.RootPath), "/")
	s.RootPath = root

	s.DatabaseBackupPath = joinPath(root, defaultIfBlank(s.DatabaseBackupPath, DefaultDatabaseBackupPath))
	s.VolumeBackupPath = joinPath(root, defaultIfBlank(s.VolumeBackupPath, DefaultVolumeBackupPath))
}

// joinPath prefixes a path with the root unless it already looks absolute under it, so
// re-normalizing is idempotent: Normalize runs on every read, and a prefix that grew
// "root/root/databases" over time would quietly orphan every earlier artifact.
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
