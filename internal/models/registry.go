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
	"time"
)

// Registry is a pull credential; its Secret is encrypted at rest and never serialized.
type Registry struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_registry_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_registry_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`
	// Empty defaults to Docker Hub.
	Server   string `json:"server"`
	Username string `json:"username"`
	// Encrypted at rest; empty when SecretRef points at the vault instead.
	Secret string `json:"-" gorm:"not null"`
	// Read from the vault at every use so rotating the secret rotates this credential; mutually exclusive with Secret.
	SecretRef string `json:"secret_ref,omitempty"`

	// The managed-by label keeps a GitOps prune from deleting a hand-created credential.
	Metadata    Metadata `json:"metadata,omitempty" gorm:"serializer:json"`
	Annotations Metadata `json:"annotations,omitempty" gorm:"serializer:json"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	HasSecret bool `json:"has_secret" gorm:"-"`
}
