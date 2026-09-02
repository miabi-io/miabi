// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// User is a global identity.
type User struct {
	ID                       uint       `json:"id" gorm:"primaryKey"`
	Name                     string     `json:"name" gorm:"not null;default:''"`
	Username                 string     `json:"username" gorm:"uniqueIndex;not null"`
	Email                    string     `json:"email" gorm:"uniqueIndex;not null"`
	PasswordHash             string     `json:"-" gorm:"not null"`
	Role                     SystemRole `json:"role" gorm:"default:user;not null"`
	TwoFactorSecret          string     `json:"-" gorm:"type:text"`
	TwoFactorEnabled         bool       `json:"two_factor_enabled" gorm:"default:false;not null"`
	Active                   bool       `json:"active" gorm:"default:true;not null"`
	MustChangePassword       bool       `json:"must_change_password" gorm:"not null;default:false"`
	AuthSource               string     `json:"auth_source" gorm:"not null;default:'local'"`
	WorkspaceLimit           *int       `json:"workspace_limit,omitempty"`
	WorkspaceMembershipLimit *int       `json:"workspace_membership_limit,omitempty"`
	ScheduledDeletionAt      *time.Time `json:"scheduled_deletion_at" gorm:"index"`
	EmailVerifiedAt          *time.Time `json:"email_verified_at"`
	LastLoginAt              *time.Time `json:"last_login_at"`
	OnboardingDismissedAt    *time.Time `json:"onboarding_dismissed_at"`
	DefaultWorkspaceID       *uint      `json:"default_workspace_id" gorm:"index"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

func (u *User) IsAdmin() bool { return u.Role == SystemRoleAdmin }

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
