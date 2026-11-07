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

// Registry storage driver values for RegistrySettings.StorageType.
const (
	RegistryStorageFilesystem = "filesystem"
	RegistryStorageS3         = "s3"
)

// RegistrySettings: only DeleteEnabled and PerWorkspaceQuotaMB are writable; the rest is environment-derived.
type RegistrySettings struct {
	ID uint `json:"id" gorm:"primaryKey"`

	// Read-only (MIABI_REGISTRY_ENABLED); off by default so single-node installs are unchanged.
	Enabled bool `json:"enabled"`
	// Read-only: MIABI_REGISTRY_HOST, else derived from the default cluster's external domain.
	Host string `json:"host,omitempty"`

	// Read-only: MIABI_REGISTRY_STORAGE; s3 also requires the registry_s3 entitlement, checked at container start.
	StorageType string `json:"storage_type" gorm:"not null;default:filesystem"`

	// Filesystem driver: the managed data volume, derived from StorageClass (see DataVolume).
	VolumeName string `json:"volume_name,omitempty"`
	// Empty means Docker's data root; read-only when MIABI_REGISTRY_STORAGE_CLASS is set.
	StorageClass string `json:"storage_class,omitempty"`

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

	S3SecretSet bool `json:"s3_secret_set" gorm:"-"`
}

// DefaultRegistryVolume is the managed data volume for the filesystem driver.
const DefaultRegistryVolume = "mb-registry-data"

// A class-backed registry gets its own volume so choosing a class never re-points existing data at an empty dir.
func (r *RegistrySettings) DataVolume() string {
	if c := NormalizeRegistryStorageClass(r.StorageClass); c != "" {
		return DefaultRegistryVolume + "-" + c
	}
	return DefaultRegistryVolume
}

// NormalizeRegistryStorageClass maps the built-in class to "", the form the settings store.
func NormalizeRegistryStorageClass(name string) string {
	name = strings.TrimSpace(name)
	if name == DefaultStorageClassName {
		return ""
	}
	return name
}

// IsRegistryDataVolume reports whether a Docker volume holds the built-in registry's blobs.
func IsRegistryDataVolume(name string) bool {
	return name == DefaultRegistryVolume || strings.HasPrefix(name, DefaultRegistryVolume+"-")
}

// UsesS3 reports whether the registry is configured for the S3 storage driver.
func (r *RegistrySettings) UsesS3() bool { return r.StorageType == RegistryStorageS3 }
