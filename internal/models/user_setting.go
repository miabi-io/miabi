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
	"slices"
	"strings"
	"time"
)

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

// A fixed set because an arbitrary hex cannot be contrast-checked against button text and semantic ramps.
type Accent string

const (
	AccentDefault Accent = "default"
	AccentBlue    Accent = "blue"
	AccentIndigo  Accent = "indigo"
	AccentSlate   Accent = "slate"
	AccentOrange  Accent = "orange"
	AccentLime    Accent = "lime"
)

func AccentCodes() []string {
	return []string{
		string(AccentDefault), string(AccentBlue), string(AccentIndigo),
		string(AccentSlate), string(AccentOrange), string(AccentLime),
	}
}

// Keep in step with web/src/theme/accents.json, which actually renders them.
func ValidAccent(a Accent) bool {
	switch a {
	case AccentDefault, AccentBlue, AccentIndigo, AccentSlate, AccentOrange, AccentLime:
		return true
	}
	return false
}

// Display languages the console ships.
const (
	LocaleEnglish = "en"
	LocaleFrench  = "fr"
)

// locale_test.go fails if this drifts from web/src/i18n/languages.ts.
var locales = []string{LocaleEnglish, LocaleFrench}

// Locales lists the display languages the console ships.
func Locales() []string {
	out := make([]string, len(locales))
	copy(out, locales)
	return out
}

// ValidLocale reports whether l is a display language the console ships.
func ValidLocale(l string) bool {
	return slices.Contains(locales, l)
}

// Rows saved while Language was free text hold arbitrary BCP 47 tags, hence the primary-subtag fallback.
func ResolveLocale(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	if ValidLocale(l) {
		return l
	}
	if i := strings.IndexAny(l, "-_"); i > 0 && ValidLocale(l[:i]) {
		return l[:i]
	}
	return LocaleEnglish
}

type UserSetting struct {
	ID     uint `json:"-" gorm:"primaryKey"`
	UserID uint `json:"-" gorm:"uniqueIndex;not null"`

	Theme Theme `json:"theme" gorm:"size:10;not null;default:system"`
	// Empty is not the default: an unset accent inherits the operator's brand accent, a chosen one does not.
	Accent       Accent `json:"accent" gorm:"size:16;not null;default:''"`
	Timezone     string `json:"timezone" gorm:"size:64;not null;default:UTC"`
	Locale       string `json:"locale" gorm:"size:10;not null;default:en"`
	LandingView  string `json:"landing_view" gorm:"size:32;not null;default:dashboard"`
	AccentLocked bool   `json:"accent_locked" gorm:"-"`

	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

func DefaultUserSetting() UserSetting {
	return UserSetting{Theme: ThemeSystem, Timezone: "UTC", Locale: LocaleEnglish, LandingView: "dashboard"}
}

func ValidLandingView(v string) bool {
	switch v {
	case "dashboard", "apps", "databases", "routes", "domains", "volumes", "jobs", "pipelines", "monitoring":
		return true
	}
	return false
}
