// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Registry storage driver values for RegistrySettings.StorageType.
const (
	RegistryStorageFilesystem = "filesystem"
	RegistryStorageS3         = "s3"
)

// RegistrySettings is the single-row, platform-scoped config for the built-in Docker registry
// (one per platform, mirroring PlatformBackupSettings). The S3 secret is encrypted at rest.
// Only DeleteEnabled and PerWorkspaceQuotaMB are writable; the rest is environment-derived.
type RegistrySettings struct {
	ID uint `json:"id" gorm:"primaryKey"`

	// Enabled runs the registry container and seeds its gateway route/middleware.
	// Read-only: MIABI_REGISTRY_ENABLED. Default false → a no-op so single-node
	// installs are unchanged.
	Enabled bool `json:"enabled"`
	// Host is the public registry hostname (e.g. registry.<external-base-domain>),
	// the docker login target. Read-only: MIABI_REGISTRY_HOST, else derived from
	// the external base domain.
	Host string `json:"host,omitempty"`

	// StorageType is "filesystem" (a managed volume) or "s3" (S3/MinIO).
	// Read-only: MIABI_REGISTRY_STORAGE. The S3 driver additionally requires the
	// registry_s3 entitlement, enforced at container start.
	StorageType string `json:"storage_type" gorm:"not null;default:filesystem"`

	// Filesystem driver: the managed data volume. A fixed platform name.
	VolumeName string `json:"volume_name,omitempty"`

	// S3 / MinIO driver. Read-only: MIABI_REGISTRY_S3_*.
	S3Endpoint       string `json:"s3_endpoint,omitempty"`
	S3Bucket         string `json:"s3_bucket,omitempty"`
	S3Region         string `json:"s3_region,omitempty"`
	S3AccessKey      string `json:"s3_access_key,omitempty"`
	S3SecretKeyEnc   string `json:"-" gorm:"column:s3_secret_key_enc"` // crypto.Encrypt (platform scope)
	S3ForcePathStyle bool   `json:"s3_force_path_style"`

	// DeleteEnabled turns on the registry's delete API (a prerequisite for GC).
	DeleteEnabled bool `json:"delete_enabled"`
	// PerWorkspaceQuotaMB caps a workspace namespace's total blob size (0 = none).
	PerWorkspaceQuotaMB int `json:"per_workspace_quota_mb"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// S3SecretSet reports whether a secret key is stored, without exposing it.
	// Not persisted; populated on read so the UI can render "••••• (set)".
	S3SecretSet bool `json:"s3_secret_set" gorm:"-"`
}

// DefaultRegistryVolume is the managed data volume for the filesystem driver.
const DefaultRegistryVolume = "mb-registry-data"

// UsesS3 reports whether the registry is configured for the S3 storage driver.
func (r *RegistrySettings) UsesS3() bool { return r.StorageType == RegistryStorageS3 }
