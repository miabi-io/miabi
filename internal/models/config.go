// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Config is a workspace-scoped set of named configuration files mounted into
// applications as read-only files. It is the file-shaped counterpart to Secret,
// and its content is always encrypted at rest: config files carry credentials
// more often than not, and the cost is negligible.
type Config struct {
	UIDModel
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index:idx_config_workspace_name,unique;not null"`
	Name        string `json:"name" gorm:"index:idx_config_workspace_name,unique;not null"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	// DataEnc is the encrypted JSON object of key -> content, never returned raw
	// when Sensitive.
	DataEnc string `json:"-" gorm:"column:data_enc;type:text"`
	// Digest is sha256 over the canonical (sorted-key) serialization of the
	// decrypted data. Drives redeploy-on-change and docker object naming.
	Digest    string `json:"digest" gorm:"not null"`
	Mode      string `json:"mode" gorm:"not null;default:0644"`
	Sensitive bool   `json:"sensitive" gorm:"not null;default:false"`
	// Delimiters overrides the interpolation delimiters for this config's values.
	Delimiters []string `json:"delimiters,omitempty" gorm:"serializer:json"`
	// Version bumps on each content change, for display and rollback.
	Version     int   `json:"version" gorm:"not null;default:1"`
	UpdatedByID *uint `json:"updated_by_id,omitempty"`

	// Managed marks a config auto-created and owned by a platform resource; its
	// lifecycle follows the owner. Mirrors Secret.
	Managed   bool   `json:"managed" gorm:"not null;default:false"`
	OwnerKind string `json:"owner_kind,omitempty" gorm:"index:idx_config_owner"`
	OwnerID   uint   `json:"owner_id,omitempty" gorm:"index:idx_config_owner"`

	Metadata  Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
