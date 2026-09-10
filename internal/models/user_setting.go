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

// Accent is the console's primary colour. A fixed set rather than a free colour:
// an arbitrary hex cannot be checked for contrast on both grounds, against white
// button text, or against the semantic ramps — a fixed set is checked once.
type Accent string

const (
	AccentDefault Accent = "default"
	AccentBlue    Accent = "blue"
	AccentIndigo  Accent = "indigo"
	AccentSlate   Accent = "slate"
	AccentOrange  Accent = "orange"
	AccentLime    Accent = "lime"
)

// AccentCodes lists every accent, so an API can offer exactly what the console can
// render rather than restating the list somewhere it can drift.
func AccentCodes() []string {
	return []string{
		string(AccentDefault), string(AccentBlue), string(AccentIndigo),
		string(AccentSlate), string(AccentOrange), string(AccentLime),
	}
}

// ValidAccent reports whether a is an accent the console ships. Keep in step with
// web/src/theme/accents.json, which is what actually renders them.
func ValidAccent(a Accent) bool {
	switch a {
	case AccentDefault, AccentBlue, AccentIndigo, AccentSlate, AccentOrange, AccentLime:
		return true
	}
	return false
}

type UserSetting struct {
	ID     uint `json:"-" gorm:"primaryKey"`
	UserID uint `json:"-" gorm:"uniqueIndex;not null"`

	Theme Theme `json:"theme" gorm:"size:10;not null;default:system"`
	// Accent is EMPTY until the user picks one, which is not the same as picking
	// the default: an unset accent inherits the operator's brand accent, and a
	// chosen one does not. Resolved on read; see usersettings.
	Accent      Accent `json:"accent" gorm:"size:16;not null;default:''"`
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
