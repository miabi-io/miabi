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

import "time"

// License: the signed Token is re-verified on every load, so the parsed columns are only a display cache.
type License struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	LicenseID   string    `json:"license_id" gorm:"uniqueIndex;not null"`
	Edition     string    `json:"edition" gorm:"not null"`
	Token       string    `json:"-" gorm:"type:text;not null"` // never serialized to the API
	Customer    string    `json:"customer"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	GraceDays   int       `json:"grace_days"`
	InstalledBy uint      `json:"installed_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
