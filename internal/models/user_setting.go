// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

type Theme string

const (
	ThemeSystem Theme = "system"
	ThemeLight  Theme = "light"
	ThemeDark   Theme = "dark"
)

// ValidTheme reports whether t is a known theme.
func ValidTheme(t Theme) bool {
	switch t {
	case ThemeSystem, ThemeLight, ThemeDark:
		return true
	}
	return false
}

type UserSetting struct {
	ID     uint `json:"-" gorm:"primaryKey"`
	UserID uint `json:"-" gorm:"uniqueIndex;not null"`

	Theme       Theme  `json:"theme" gorm:"size:10;not null;default:system"`
	Timezone    string `json:"timezone" gorm:"size:64;not null;default:UTC"`
	Locale      string `json:"locale" gorm:"size:10;not null;default:en"`
	LandingView string `json:"landing_view" gorm:"size:32;not null;default:dashboard"`

	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

func DefaultUserSetting() UserSetting {
	return UserSetting{Theme: ThemeSystem, Timezone: "UTC", Locale: "en", LandingView: "dashboard"}
}

func ValidLandingView(v string) bool {
	switch v {
	case "dashboard", "apps", "databases", "routes", "domains", "volumes", "jobs", "pipelines", "monitoring":
		return true
	}
	return false
}
