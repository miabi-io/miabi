// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// SettingType describes how a setting's string value should be interpreted.
type SettingType string

const (
	SettingTypeString SettingType = "string"
	SettingTypeInt    SettingType = "int"
	SettingTypeBool   SettingType = "bool"
	SettingTypeJSON   SettingType = "json"
)

// Setting is a single global, platform-wide key/value configuration entry. The
// value is always stored as a string; Type records how to parse it.
type Setting struct {
	ID        uint        `json:"id" gorm:"primaryKey"`
	Key       string      `json:"key" gorm:"uniqueIndex;not null"`
	Value     string      `json:"value" gorm:"type:text"`
	Type      SettingType `json:"type" gorm:"default:string;not null"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`

	// Pinned reports that an environment variable supplies this value. Not
	// persisted: populated on read so the console can show the field as
	// operator-managed instead of offering an edit that reverts on restart.
	Pinned bool `json:"pinned" gorm:"-"`
}
