// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Resource types a per-resource policy may target.
const (
	ResourceTypeApp      = "app"
	ResourceTypeDatabase = "database"
	ResourceTypeDomain   = "domain"
)

// ResourcePolicy grants a user permissions on a single resource (Enterprise, gated on
// resource_policies for writes). It augments the workspace role: a member may be a viewer
// everywhere yet hold app:deploy on one specific app. The table is empty in Community.
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
