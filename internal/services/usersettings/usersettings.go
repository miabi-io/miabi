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

// Package usersettings owns per-user preferences, separate from account lifecycle.
package usersettings

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

var (
	ErrNotMember          = errors.New("you are not a member of this workspace")
	ErrInvalidTheme       = errors.New("theme must be one of: system, light, dark")
	ErrInvalidAccent      = errors.New("accent must be one of: default, blue, indigo, slate, orange, lime")
	ErrInvalidLocale      = fmt.Errorf("locale must be one of: %s", strings.Join(models.Locales(), ", "))
	ErrInvalidLandingView = errors.New("landing view is not a known console section")
	ErrInvalidTimezone    = errors.New("timezone must be an IANA time zone name, such as Europe/Paris")
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

// A function rather than the branding service, so preferences don't depend on branding.
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

// The default is not stored, so a brand accent change still moves everyone who never picked.
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
	var tz string
	if in.Timezone != nil {
		tz = strings.TrimSpace(*in.Timezone)
		if tz != "" && !ValidTimezone(tz) {
			return nil, ErrInvalidTimezone
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

	if tz != "" {
		cur.Timezone = tz
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

// "Local" is refused: it names the server's zone, not the user's.
func ValidTimezone(tz string) bool {
	if tz == "" || tz == "Local" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}
