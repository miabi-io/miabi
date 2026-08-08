// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Secret is a workspace-scoped named secret (the Vault). Env values reference it by name
// (`${{ secrets.NAME }}`); the reference is resolved into a container's environment at
// deploy/job time and never returned by the API. The value is encrypted at rest.
type Secret struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_secret_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace; env refs use it.
	Name string `json:"name" gorm:"index:idx_secret_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`
	ValueEnc    string `json:"-" gorm:"column:value_enc;type:text"` // encrypted
	Description string `json:"description"`
	// Version bumps on each value change (rotation), for display/rollback.
	Version     int   `json:"version" gorm:"not null;default:1"`
	UpdatedByID *uint `json:"updated_by_id,omitempty"`

	// Managed marks a secret auto-created and owned by a platform resource (e.g.
	// a managed database). Its value is derived (rotate via the owner, not by
	// hand) and its lifecycle follows the owner. OwnerKind/OwnerID identify it.
	Managed   bool   `json:"managed" gorm:"not null;default:false"`
	OwnerKind string `json:"owner_kind,omitempty" gorm:"index:idx_secret_owner"`
	OwnerID   uint   `json:"owner_id,omitempty" gorm:"index:idx_secret_owner"`

	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata  Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
