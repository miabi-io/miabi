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

// BaseRole gives a rank so the legacy RequireRole still resolves; the table is empty in Community.
type CustomRole struct {
	ID          uint          `json:"id" gorm:"primaryKey"`
	WorkspaceID *uint         `json:"workspace_id" gorm:"index"`
	Name        string        `json:"name" gorm:"not null"`
	BaseRole    WorkspaceRole `json:"base_role" gorm:"not null"`
	Permissions []string      `json:"permissions" gorm:"serializer:json"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// PermissionSet returns the role's permissions as a lookup set.
func (r *CustomRole) PermissionSet() map[Permission]bool {
	out := make(map[Permission]bool, len(r.Permissions))
	for _, p := range r.Permissions {
		out[Permission(p)] = true
	}
	return out
}
