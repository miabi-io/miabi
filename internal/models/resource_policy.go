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

// Resource types a per-resource policy may target.
const (
	ResourceTypeApp      = "app"
	ResourceTypeDatabase = "database"
	ResourceTypeDomain   = "domain"
)

// ResourcePolicy augments the workspace role with per-resource grants (Enterprise); empty in Community.
type ResourcePolicy struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	WorkspaceID  uint      `json:"workspace_id" gorm:"index:idx_resource_policy;not null"`
	UserID       uint      `json:"user_id" gorm:"index:idx_resource_policy;not null"`
	ResourceType string    `json:"resource_type" gorm:"index:idx_resource_policy;not null"`
	ResourceID   uint      `json:"resource_id" gorm:"index:idx_resource_policy;not null"`
	Permissions  []string  `json:"permissions" gorm:"serializer:json"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	User User `json:"user,omitempty" gorm:"foreignKey:UserID"`
}

// Grants reports whether the policy includes a permission.
func (p *ResourcePolicy) Grants(perm Permission) bool {
	for _, x := range p.Permissions {
		if Permission(x) == perm {
			return true
		}
	}
	return false
}

// IsValidResourceType reports whether t is a supported resource type.
func IsValidResourceType(t string) bool {
	switch t {
	case ResourceTypeApp, ResourceTypeDatabase, ResourceTypeDomain:
		return true
	default:
		return false
	}
}
