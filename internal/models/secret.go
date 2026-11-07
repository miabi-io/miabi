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

// Secret values are encrypted at rest; ${{ secrets.NAME }} resolves at deploy/job time and is never returned by the API.
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

	// Owned by a platform resource: rotate via the owner, not by hand; lifecycle follows the owner.
	Managed   bool   `json:"managed" gorm:"not null;default:false"`
	OwnerKind string `json:"owner_kind,omitempty" gorm:"index:idx_secret_owner"`
	OwnerID   uint   `json:"owner_id,omitempty" gorm:"index:idx_secret_owner"`

	// Lets a changed sealed value in git plan as an update; both clear when the value is set any other way.
	SealedFP         string `json:"-" gorm:"column:sealed_fp"`
	SealedKeyVersion int    `json:"sealed_key_version,omitempty" gorm:"not null;default:0"`

	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata  Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
