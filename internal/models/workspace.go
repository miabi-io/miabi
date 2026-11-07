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

// WorkspaceRole is a member's role within a workspace.
type WorkspaceRole string

const (
	WorkspaceRoleOwner     WorkspaceRole = "owner"
	WorkspaceRoleAdmin     WorkspaceRole = "admin"
	WorkspaceRoleDeveloper WorkspaceRole = "developer"
	WorkspaceRoleViewer    WorkspaceRole = "viewer"
)

// roleRank orders roles from least to most privileged.
var roleRank = map[WorkspaceRole]int{
	WorkspaceRoleViewer:    1,
	WorkspaceRoleDeveloper: 2,
	WorkspaceRoleAdmin:     3,
	WorkspaceRoleOwner:     4,
}

// Rank returns the privilege level of a role (0 if unknown).
func (r WorkspaceRole) Rank() int { return roleRank[r] }

// AtLeast reports whether r is at least as privileged as min.
func (r WorkspaceRole) AtLeast(min WorkspaceRole) bool { return r.Rank() >= min.Rank() }

// Valid reports whether r is a known role.
func (r WorkspaceRole) Valid() bool { _, ok := roleRank[r]; return ok }

// InvitationStatus is the lifecycle state of a workspace invitation.
type InvitationStatus string

const (
	InvitationStatusPending  InvitationStatus = "pending"
	InvitationStatusAccepted InvitationStatus = "accepted"
	InvitationStatusRevoked  InvitationStatus = "revoked"
)

// Workspace is the multi-tenant root that owns all resources.
type Workspace struct {
	UIDModel
	ID   uint   `json:"id" gorm:"primaryKey"`
	Name string `json:"name" gorm:"uniqueIndex;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	OwnerID     uint   `json:"owner_id" gorm:"index;not null"`
	// Nullable so SSO/SCIM can scope to an org without a destructive migration when multi-org lands.
	OrganizationID *uint `json:"organization_id" gorm:"index"`
	Privileged     bool  `json:"privileged" gorm:"not null;default:false"`
	// Created on first boot, always privileged, undeletable, and managed only by platform admins.
	System bool `json:"system" gorm:"not null;default:false"`
	// nil means the default plan, then unlimited.
	PlanID *uint `json:"plan_id,omitempty" gorm:"index"`
	// DefaultClusterID is the location a create lands in when it names none; nil means the default cluster.
	DefaultClusterID *uint `json:"default_cluster_id,omitempty" gorm:"index"`
	// Restriction is a platform admin's hold on the workspace (empty = none). See WorkspaceRestriction.
	Restriction       WorkspaceRestriction `json:"restriction" gorm:"type:varchar(20);not null;default:''"`
	RestrictionReason string               `json:"restriction_reason"`
	RestrictionUntil  *time.Time           `json:"restriction_until"`
	RestrictedAt      *time.Time           `json:"restricted_at"`
	RestrictedBy      *uint                `json:"restricted_by"`
	// Lets lifting the suspension restart exactly that, not things a user stopped on purpose.
	FrozenResources *FrozenResources `json:"-" gorm:"type:text;serializer:json"`
	// BillingAccountID is the billing account the workspace is billed to (nil = not billed).
	BillingAccountID *uint `json:"billing_account_id,omitempty" gorm:"index"`
	// BillingPrevPlanID is the plan the workspace had before it was attached, restored on detach.
	BillingPrevPlanID *uint     `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`

	Members []WorkspaceMember `json:"-" gorm:"foreignKey:WorkspaceID"`
}

// WorkspaceRestriction is a platform admin's hold on a workspace.
type WorkspaceRestriction string

const (
	WorkspaceRestrictionNone WorkspaceRestriction = ""
	// WorkspaceRestrictionReadOnly keeps workloads running but refuses writes and automated deploys.
	WorkspaceRestrictionReadOnly WorkspaceRestriction = "read_only"
	// WorkspaceRestrictionSuspended is read-only with apps and databases stopped and CronJobs paused.
	WorkspaceRestrictionSuspended WorkspaceRestriction = "suspended"
	// Databases keep running so they can be exported; only a migration sets or lifts it.
	WorkspaceRestrictionFrozen WorkspaceRestriction = "frozen"
)

// Valid reports whether r is a restriction an admin can set.
func (r WorkspaceRestriction) Valid() bool {
	return r == WorkspaceRestrictionReadOnly || r == WorkspaceRestrictionSuspended
}

// FrozenResources lists the apps and database instances a suspension stopped.
type FrozenResources struct {
	Apps      []uint `json:"apps"`
	Databases []uint `json:"databases"`
}

type WorkspaceWithRole struct {
	Workspace
	Role WorkspaceRole `json:"role"`
}

// WorkspaceMember links a user to a workspace with a role.
type WorkspaceMember struct {
	ID          uint          `json:"id" gorm:"primaryKey"`
	WorkspaceID uint          `json:"workspace_id" gorm:"uniqueIndex:idx_workspace_user;not null"`
	UserID      uint          `json:"user_id" gorm:"uniqueIndex:idx_workspace_user;not null"`
	Role        WorkspaceRole `json:"role" gorm:"not null;default:viewer"`
	// Role still holds the custom role's BaseRole for rank checks.
	CustomRoleID *uint `json:"custom_role_id" gorm:"index"`
	// Such memberships are changed and removed only by that provider's group sync.
	ManagedBy string    `json:"managed_by" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	User User `json:"user" gorm:"foreignKey:UserID"`
}

// WorkspaceInvitation is a pending invite for an email to join a workspace.
type WorkspaceInvitation struct {
	ID          uint             `json:"id" gorm:"primaryKey"`
	WorkspaceID uint             `json:"workspace_id" gorm:"index;not null"`
	Email       string           `json:"email" gorm:"not null;index"`
	Role        WorkspaceRole    `json:"role" gorm:"not null;default:viewer"`
	TokenHash   string           `json:"-" gorm:"uniqueIndex;not null"`
	Status      InvitationStatus `json:"status" gorm:"not null;default:pending"`
	InvitedBy   uint             `json:"invited_by" gorm:"not null"`
	ExpiresAt   time.Time        `json:"expires_at" gorm:"not null"`
	CreatedAt   time.Time        `json:"created_at"`
}
