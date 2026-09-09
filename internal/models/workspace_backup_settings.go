// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

type WorkspaceBackupSettings struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"uniqueIndex;not null"`

	S3Enabled        bool   `json:"s3_enabled"`
	S3Endpoint       string `json:"s3_endpoint,omitempty"`
	S3Bucket         string `json:"s3_bucket,omitempty"`
	S3Region         string `json:"s3_region,omitempty"`
	S3AccessKey      string `json:"s3_access_key,omitempty"`
	S3SecretKeyEnc   string `json:"-" gorm:"column:s3_secret_key_enc"` // encrypted at rest
	S3UseSSL         bool   `json:"s3_use_ssl"`
	S3ForcePathStyle bool   `json:"s3_force_path_style"`

	// Path prefixes within the bucket (one shared bucket, three prefixes).
	DatabaseBackupPath string `json:"database_backup_path,omitempty"`
	VolumeBackupPath   string `json:"volume_backup_path,omitempty"`
	BundlePath         string `json:"bundle_path,omitempty"`

	// BundlePassphraseEnc is the bundle passphrase at rest, encrypted with the
	// workspace's data-encryption key.
	BundlePassphraseEnc string `json:"-" gorm:"column:bundle_passphrase_enc"`

	// BackupPassphraseEnc encrypts database backup artifacts, at rest itself under the
	// workspace's data-encryption key. Set means every new database backup leaves the
	// platform as GPG ciphertext; unset preserves the previous cleartext behaviour.
	BackupPassphraseEnc string `json:"-" gorm:"column:backup_passphrase_enc"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// S3SecretSet reports whether a secret key is stored, without exposing it.
	// Not persisted; populated on read so the UI can render "••••• (set)".
	S3SecretSet         bool `json:"s3_secret_set" gorm:"-"`
	BundlePassphraseSet bool `json:"bundle_passphrase_set" gorm:"-"`
	BackupPassphraseSet bool `json:"backup_passphrase_set" gorm:"-"`
}

// DefaultBundlePath is where bundles live when the workspace sets no prefix.
const DefaultBundlePath = "bundles"

// BundlePrefix is the effective bundle prefix for the workspace.
func (s *WorkspaceBackupSettings) BundlePrefix() string {
	if s.BundlePath != "" {
		return s.BundlePath
	}
	return DefaultBundlePath
}
