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
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/slug"
	"gorm.io/gorm"
)

type SystemRole string

const (
	SystemRoleAdmin SystemRole = "admin" // platform super-admin
	SystemRoleUser  SystemRole = "user"
)

// Non-local providers stay the source of truth for the account's name and handle.
const (
	AuthSourceLocal = "local"
	AuthSourceOAuth = "oauth"
	AuthSourceLDAP  = "ldap"
	AuthSourceSAML  = "saml"
	AuthSourceSCIM  = "scim"
)

// A service account signs in only with API keys, never a password, SSO or console session.
const (
	UserKindHuman   = "user"
	UserKindService = "service"
)

// Reserved RFC 2606 domain so no identity provider or mailbox can ever claim one.
const ServiceAccountEmailDomain = "service-accounts.invalid"

// User is a global identity.
type User struct {
	ID               uint       `json:"id" gorm:"primaryKey"`
	Name             string     `json:"name" gorm:"not null;default:''"`
	Username         string     `json:"username" gorm:"uniqueIndex;not null"`
	Email            string     `json:"email" gorm:"uniqueIndex;not null"`
	PasswordHash     string     `json:"-" gorm:"not null"`
	Role             SystemRole `json:"role" gorm:"default:user;not null"`
	TwoFactorSecret  string     `json:"-" gorm:"type:text"`
	TwoFactorEnabled bool       `json:"two_factor_enabled" gorm:"default:false;not null"`
	Active           bool       `json:"active" gorm:"default:true;not null"`
	// The user keeps read access but every write is refused until lifted or SuspendedUntil passes.
	SuspendedAt        *time.Time `json:"suspended_at"`
	SuspendedReason    string     `json:"suspended_reason"`
	SuspendedUntil     *time.Time `json:"suspended_until"`
	SuspendedBy        *uint      `json:"suspended_by"`
	MustChangePassword bool       `json:"must_change_password" gorm:"not null;default:false"`
	AuthSource         string     `json:"auth_source" gorm:"not null;default:'local'"`
	Kind               string     `json:"kind" gorm:"not null;default:'user';index"`
	// ServiceWorkspaceID is the workspace that owns a service account; its admins manage it.
	ServiceWorkspaceID       *uint `json:"service_workspace_id,omitempty" gorm:"index"`
	OrganizationID           *uint `json:"organization_id,omitempty" gorm:"index"`
	WorkspaceLimit           *int  `json:"workspace_limit,omitempty"`
	WorkspaceMembershipLimit *int  `json:"workspace_membership_limit,omitempty"`
	// nil inherits the organization's workspace creation mode.
	CanCreateWorkspaces   *bool      `json:"can_create_workspaces,omitempty"`
	SignupSource          string     `json:"signup_source" gorm:"not null;default:''"`
	ScheduledDeletionAt   *time.Time `json:"scheduled_deletion_at" gorm:"index"`
	EmailVerifiedAt       *time.Time `json:"email_verified_at"`
	LastLoginAt           *time.Time `json:"last_login_at"`
	OnboardingDismissedAt *time.Time `json:"onboarding_dismissed_at"`
	DefaultWorkspaceID    *uint      `json:"default_workspace_id" gorm:"index"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// IsAdmin is never true for a service account, whose keys would otherwise carry platform-wide power.
func (u *User) IsAdmin() bool { return u.Role == SystemRoleAdmin && !u.IsService() }

func (u *User) Suspended(now time.Time) bool {
	return u.SuspendedAt != nil && (u.SuspendedUntil == nil || u.SuspendedUntil.After(now))
}

func (u *User) IsService() bool { return u.Kind == UserKindService }

// IsExternal profiles are not user-editable: the provider re-asserts name and handle on every sign-in.
func (u *User) IsExternal() bool {
	src := strings.ToLower(strings.TrimSpace(u.AuthSource))
	return src != "" && src != AuthSourceLocal
}

// AfterCreate makes the first user own an ownerless org; the owner_user_id = 0 predicate prevents a double claim.
func (u *User) AfterCreate(tx *gorm.DB) error {
	if u.OrganizationID == nil || *u.OrganizationID == 0 {
		return nil
	}
	_ = tx.Model(&Organization{}).
		Where("id = ? AND owner_user_id = ?", *u.OrganizationID, 0).
		Update("owner_user_id", u.ID).Error
	return nil
}

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.SignupSource == "" && u.IsExternal() {
		u.SignupSource = strings.ToLower(strings.TrimSpace(u.AuthSource))
	}
	if h := slug.Make(u.Username, ""); h != "" {
		u.Username = h
		return nil
	}
	base := u.Email
	if at := strings.IndexByte(base, '@'); at >= 0 {
		base = base[:at]
	}
	username, err := slug.UniqueAvailable(base, "user", func(candidate string) (bool, error) {
		var count int64
		if err := tx.Model(&User{}).Where("username = ?", candidate).Count(&count).Error; err != nil {
			return false, err
		}
		return count > 0, nil
	})
	if err != nil {
		return err
	}
	u.Username = username
	return nil
}
