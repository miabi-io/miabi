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

// Middleware Rule holds the type-specific configuration exactly as Goma defines it.
type Middleware struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_mw_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace (Goma middleware name).
	Name string `json:"name" gorm:"index:idx_mw_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string                 `json:"display_name"`
	Type        string                 `json:"type" gorm:"not null"` // Goma middleware kind, e.g. basicAuth, rateLimit
	Paths       []string               `json:"paths,omitempty" gorm:"serializer:json"`
	Rule        map[string]interface{} `json:"rule,omitempty" gorm:"serializer:json"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
}
