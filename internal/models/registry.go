// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"time"
)

// Registry is a stored container-registry credential used to pull private
// images at deploy time. The Secret (password / access token) is encrypted at
// rest and never serialized.
type Registry struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_registry_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_registry_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`
	// Server is the registry host, e.g. "registry-1.docker.io", "ghcr.io",
	// "registry.gitlab.com". Empty defaults to Docker Hub.
	Server   string `json:"server"`
	Username string `json:"username"`
	// Secret holds the password or access token, encrypted at rest. Empty when the
	// credential points at the vault instead (see SecretRef).
	Secret string `json:"-" gorm:"not null"`
	// SecretRef names a workspace Secret holding the password instead of storing a copy here. The
	// value is read from the vault at every use, so rotating that secret rotates this credential.
	// Mutually exclusive with Secret; a name, so the API returns it.
	SecretRef string `json:"secret_ref,omitempty"`

	// Metadata holds free-form labels (provenance, grouping, GitOps). Keys under the reserved
	// "miabi.io/" prefix are platform-managed — the managed-by label is what keeps a GitOps prune
	// from deleting a credential created by hand.
	Metadata Metadata `json:"metadata,omitempty" gorm:"serializer:json"`
	// Annotations holds free-form, non-identifying descriptive metadata (the
	// manifest's metadata.annotations); no reserved keys. Persisted as JSON.
	Annotations Metadata `json:"annotations,omitempty" gorm:"serializer:json"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// HasSecret is a transient flag for responses (never persisted): true when the
	// credential can authenticate, whether from its own stored secret or a vault
	// reference.
	HasSecret bool `json:"has_secret" gorm:"-"`
}
