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

// Always encrypted at rest: config files carry credentials more often than not.
type Config struct {
	UIDModel
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index:idx_config_workspace_name,unique;not null"`
	Name        string `json:"name" gorm:"index:idx_config_workspace_name,unique;not null"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	DataEnc     string `json:"-" gorm:"column:data_enc;type:text"`
	// Drives redeploy-on-change and docker object naming.
	Digest    string `json:"digest" gorm:"not null"`
	Mode      string `json:"mode" gorm:"not null;default:0644"`
	Sensitive bool   `json:"sensitive" gorm:"not null;default:false"`
	// Delimiters overrides the interpolation delimiters for this config's values.
	Delimiters []string `json:"delimiters,omitempty" gorm:"serializer:json"`
	// Version bumps on each content change, for display and rollback.
	Version     int   `json:"version" gorm:"not null;default:1"`
	UpdatedByID *uint `json:"updated_by_id,omitempty"`

	Managed   bool   `json:"managed" gorm:"not null;default:false"`
	OwnerKind string `json:"owner_kind,omitempty" gorm:"index:idx_config_owner"`
	OwnerID   uint   `json:"owner_id,omitempty" gorm:"index:idx_config_owner"`

	Metadata  Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
