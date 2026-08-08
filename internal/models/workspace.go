// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	ID uint `json:"id" gorm:"primaryKey"`
	// Name is the unique, URL/CLI/docker handle (lowercase [a-z0-9-]). It is the
	// human key for scoped routes and the registry namespace. Renamed from the
	// former "slug"; the numeric ID/UID remain the stable internal references.
	Name string `json:"name" gorm:"uniqueIndex;not null"`
	// DisplayName is the free-text label shown in the UI. Renamed from the former
	// "name"; not unique.
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	OwnerID     uint   `json:"owner_id" gorm:"index;not null"`
	// OrganizationID is the realm this workspace belongs to (nullable → the
	// default org). Lets SSO/enforced-login/SCIM scope to an org without a
	// destructive migration when multi-org lands.
	OrganizationID *uint `json:"organization_id" gorm:"index"`
	// Privileged grants trusted capabilities — currently, host port-binding
	// requests are auto-approved (range and conflict checks still apply). Only a
	// platform admin can set it.
	Privileged bool `json:"privileged" gorm:"not null;default:false"`
	// System marks the built-in platform workspace ("Miabi System"). It holds platform-managed
	// apps such as the per-node Goma gateways, is created on first boot, is always privileged,
	// and cannot be deleted. Only platform admins manage it.
	System bool `json:"system" gorm:"not null;default:false"`
	// PlanID is the assigned plan (nil → the default plan → unlimited). Drives
	// per-workspace resource quotas when plan enforcement is enabled.
	PlanID    *uint     `json:"plan_id,omitempty" gorm:"index"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Members []WorkspaceMember `json:"-" gorm:"foreignKey:WorkspaceID"`
}

// WorkspaceWithRole is a workspace annotated with the requesting user's role in
// it. Returned by the list endpoint so the UI can render role badges and gate
// admin-only affordances without an extra round-trip per workspace.
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
	// CustomRoleID, when set, overrides Role with an admin-defined permission set
	// (Enterprise). Role still holds the custom role's BaseRole for rank checks.
	CustomRoleID *uint     `json:"custom_role_id" gorm:"index"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

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
