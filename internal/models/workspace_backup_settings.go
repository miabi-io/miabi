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
	// BackupEncryption picks how database and volume backups are encrypted: "" or "gpg" uses the
	// backup passphrase; "age" encrypts to AgeRecipients, so taking a backup needs no secret and
	// only the holder of the matching identity can read it back.
	BackupEncryption string `json:"backup_encryption,omitempty"`
	// AgeRecipients are the age public keys (age1…, ssh-ed25519 …), one per line.
	AgeRecipients string `json:"age_recipients,omitempty" gorm:"type:text"`
	// AgeIdentityEnc is the age identity at rest, under the workspace's data-encryption key.
	// Optional: without it the platform cannot restore or verify age backups on its own.
	AgeIdentityEnc string `json:"-" gorm:"column:age_identity_enc;type:text"`
	// SkipBackupVerify turns off reading each backup back right after it is written.
	SkipBackupVerify bool `json:"skip_backup_verify" gorm:"not null;default:false"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// S3SecretSet reports whether a secret key is stored, without exposing it.
	// Not persisted; populated on read so the UI can render "••••• (set)".
	S3SecretSet         bool `json:"s3_secret_set" gorm:"-"`
	BundlePassphraseSet bool `json:"bundle_passphrase_set" gorm:"-"`
	BackupPassphraseSet bool `json:"backup_passphrase_set" gorm:"-"`
	AgeIdentitySet      bool `json:"age_identity_set" gorm:"-"`
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
