// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package usersettings owns per-user preferences: the console's look and locale,
// and which workspace a client lands on when it has none in mind. It is deliberately
// separate from the account package, which handles account lifecycle rather than taste.
package usersettings

import (
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

var (
	ErrNotMember          = errors.New("you are not a member of this workspace")
	ErrInvalidTheme       = errors.New("theme must be one of: system, light, dark")
	ErrInvalidLandingView = errors.New("landing view is not a known console section")
)

type Members interface {
	FindMember(workspaceID, userID uint) (*models.WorkspaceMember, error)
}

type Users interface {
	DefaultWorkspace(userID uint) (*models.WorkspaceWithRole, error)
	SetDefaultWorkspace(userID uint, workspaceID *uint) error
	FindByID(id uint) (*models.User, error)
}

type Settings interface {
	Get(userID uint) (*models.UserSetting, error)
	Save(s *models.UserSetting) error
}

type Service struct {
	users    Users
	settings Settings
	members  Members
}

func NewService(users Users, settings Settings, members Members) *Service {
	return &Service{users: users, settings: settings, members: members}
}

// Get returns a user's preferences, defaulted when they have never saved any.
func (s *Service) Get(userID uint) (*models.UserSetting, error) {
	return s.settings.Get(userID)
}

type Update struct {
	Theme       *string
	Timezone    *string
	Locale      *string
	LandingView *string
}

func (s *Service) Save(userID uint, in Update) (*models.UserSetting, error) {
	var theme models.Theme
	if in.Theme != nil {
		theme = models.Theme(strings.ToLower(strings.TrimSpace(*in.Theme)))
		if !models.ValidTheme(theme) {
			return nil, ErrInvalidTheme
		}
	}
	var view string
	if in.LandingView != nil {
		view = strings.ToLower(strings.TrimSpace(*in.LandingView))
		if !models.ValidLandingView(view) {
			return nil, ErrInvalidLandingView
		}
	}
	cur, err := s.settings.Get(userID)
	if err != nil {
		return nil, err
	}
	cur.UserID = userID
	if in.Theme != nil {
		cur.Theme = theme
	}
	if in.LandingView != nil {
		cur.LandingView = view
	}

	if in.Timezone != nil {
		if tz := strings.TrimSpace(*in.Timezone); tz != "" {
			cur.Timezone = tz
		}
	}
	if in.Locale != nil {
		if l := strings.TrimSpace(*in.Locale); l != "" {
			cur.Locale = l
		}
	}
	if err := s.settings.Save(cur); err != nil {
		return nil, err
	}
	return cur, nil
}

func (s *Service) DefaultWorkspace(userID uint) (*models.WorkspaceWithRole, error) {
	return s.users.DefaultWorkspace(userID)
}

func (s *Service) SetDefaultWorkspace(userID uint, workspaceID *uint) error {
	if workspaceID != nil {
		if s.members == nil {
			return ErrNotMember
		}
		if _, err := s.members.FindMember(*workspaceID, userID); err != nil {
			return ErrNotMember
		}
	}
	return s.users.SetDefaultWorkspace(userID, workspaceID)
}

func (s *Service) AdoptFirstWorkspace(userID, workspaceID uint) {
	user, err := s.users.FindByID(userID)
	if err != nil || user.DefaultWorkspaceID != nil {
		return
	}
	_ = s.users.SetDefaultWorkspace(userID, &workspaceID)
}
