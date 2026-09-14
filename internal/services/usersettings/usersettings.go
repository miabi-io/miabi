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
	ErrInvalidAccent      = errors.New("accent must be one of: default, blue, indigo, slate, orange, lime")
	ErrInvalidLocale      = errors.New("locale must be one of: en, fr")
	ErrInvalidLandingView = errors.New("landing view is not a known console section")
	ErrAccentLocked       = errors.New("the accent is set by your organization")
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
	users       Users
	settings    Settings
	members     Members
	brandAccent BrandAccent
}

func NewService(users Users, settings Settings, members Members) *Service {
	return &Service{users: users, settings: settings, members: members}
}

// BrandAccent supplies the operator's accent and whether it is enforced on every
// account. A function rather than the branding service itself, so preferences do
// not depend on branding — the arrow points one way.
type BrandAccent func() (accent models.Accent, enforced bool)

// SetBrandAccent wires the operator default (nil-safe: unset means Miabi's own).
func (s *Service) SetBrandAccent(f BrandAccent) { s.brandAccent = f }

// Get returns a user's preferences, defaulted when they have never saved any.
func (s *Service) Get(userID uint) (*models.UserSetting, error) {
	cur, err := s.settings.Get(userID)
	if err != nil {
		return nil, err
	}
	s.resolve(cur)
	return cur, nil
}

func (s *Service) brand() (models.Accent, bool) {
	if s.brandAccent == nil {
		return "", false
	}
	a, enforced := s.brandAccent()
	if !models.ValidAccent(a) {
		a = ""
	}
	return a, enforced
}

// resolve fills in what the user has not chosen. The stored value stays empty, so
// an operator changing their brand accent still moves everyone who never picked —
// which is the point, and would not happen if the default were written down at
// account creation.
func (s *Service) resolve(cur *models.UserSetting) {
	if cur == nil {
		return
	}
	cur.Locale = models.ResolveLocale(cur.Locale)
	brand, enforced := s.brand()
	// Enforcement replaces the pick on read only, so lifting it gives the pick back.
	if enforced || cur.Accent == "" {
		cur.Accent = brand
	}
	if cur.Accent == "" {
		cur.Accent = models.AccentDefault
	}
	cur.AccentLocked = enforced
}

type Update struct {
	Theme       *string
	Accent      *string
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
	var accent models.Accent
	if in.Accent != nil {
		accent = models.Accent(strings.ToLower(strings.TrimSpace(*in.Accent)))
		if !models.ValidAccent(accent) {
			return nil, ErrInvalidAccent
		}
		if _, enforced := s.brand(); enforced {
			return nil, ErrAccentLocked
		}
	}
	var locale string
	if in.Locale != nil {
		locale = strings.ToLower(strings.TrimSpace(*in.Locale))
		if locale != "" && !models.ValidLocale(locale) {
			return nil, ErrInvalidLocale
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
	if in.Accent != nil {
		cur.Accent = accent
	}
	if in.LandingView != nil {
		cur.LandingView = view
	}

	if in.Timezone != nil {
		if tz := strings.TrimSpace(*in.Timezone); tz != "" {
			cur.Timezone = tz
		}
	}
	if locale != "" {
		cur.Locale = locale
	}
	if err := s.settings.Save(cur); err != nil {
		return nil, err
	}
	s.resolve(cur)
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
