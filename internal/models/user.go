// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/slug"
	"gorm.io/gorm"
)

// SystemRole is a platform-wide role (distinct from per-workspace roles).
type SystemRole string

const (
	SystemRoleAdmin SystemRole = "admin" // platform super-admin
	SystemRoleUser  SystemRole = "user"
)

// User is a global identity.
type User struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// Name is the free-text display name.
	Name string `json:"name" gorm:"not null;default:''"`
	// Username is the unique, directory-friendly handle (lowercase [a-z0-9-]),
	// the join key a future LDAP/OIDC uid maps onto and a potential per-user
	// registry namespace. Distinct from Email (the login/contact). Auto-derived
	// from the email local-part on create when left blank (see BeforeCreate).
	Username     string     `json:"username" gorm:"uniqueIndex;not null"`
	Email        string     `json:"email" gorm:"uniqueIndex;not null"`
	PasswordHash string     `json:"-" gorm:"not null"`
	Role         SystemRole `json:"role" gorm:"default:user;not null"`
	// TwoFactorSecret holds the TOTP secret, encrypted at rest (crypto package).
	// Never serialized. TwoFactorEnabled is only set once a code is confirmed.
	TwoFactorSecret    string `json:"-" gorm:"type:text"`
	TwoFactorEnabled   bool   `json:"two_factor_enabled" gorm:"default:false;not null"`
	Active             bool   `json:"active" gorm:"default:true;not null"`
	MustChangePassword bool   `json:"must_change_password" gorm:"not null;default:false"`
	// AuthSource records how the account authenticates: "local" (password), or an
	// external directory/IdP ("ldap", "saml", "oauth"). Directory-managed accounts
	// carry an unusable local password and have their access reconciled on login.
	AuthSource string `json:"auth_source" gorm:"not null;default:'local'"`
	// WorkspaceLimit is an Enterprise per-user override of how many workspaces
	// this user may own, superseding the platform-global max_workspaces_per_user.
	// nil = inherit the platform limit; -1 = unlimited; 0 = none; N = at most N.
	// Only a platform admin sets it, and only with the user_workspace_limit
	// entitlement (existing overrides stay enforced read-only if the license lapses).
	WorkspaceLimit *int `json:"workspace_limit,omitempty"`
	// WorkspaceMembershipLimit is the Enterprise per-user override of how many
	// workspaces this user may JOIN as a non-owner member (invites + SSO/SCIM
	// auto-join), superseding the platform-global max_workspace_memberships_per_user.
	// Same convention: nil = inherit; -1 = unlimited; 0 = none; N = at most N.
	// Gated by the user_workspace_membership_limit entitlement.
	WorkspaceMembershipLimit *int `json:"workspace_membership_limit,omitempty"`
	// ScheduledDeletionAt, when set, is the time this account is permanently
	// purged (with all its data). The admin schedules it; a daily job purges due
	// accounts; an admin can cancel before then. The account stays disabled
	// (Active=false) throughout the grace window.
	ScheduledDeletionAt *time.Time `json:"scheduled_deletion_at" gorm:"index"`
	EmailVerifiedAt     *time.Time `json:"email_verified_at"`
	LastLoginAt         *time.Time `json:"last_login_at"`
	// OnboardingDismissedAt, when set, is when the user dismissed (or completed)
	// the getting-started checklist. Nil = still show onboarding guidance.
	OnboardingDismissedAt *time.Time `json:"onboarding_dismissed_at"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// IsAdmin reports whether the user is a platform super-admin.
func (u *User) IsAdmin() bool { return u.Role == SystemRoleAdmin }

// BeforeCreate derives a unique username when one was not supplied, so every
// creation path (seed, admin-create, OAuth/SAML auto-provision, SCIM) gets a
// valid handle without repeating the logic. The base is the email local-part,
// slugified; collisions and reserved words are resolved by numeric suffixing.
// An explicitly supplied username is normalized but otherwise trusted (callers
// validate user-chosen handles up front).
func (u *User) BeforeCreate(tx *gorm.DB) error {
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
