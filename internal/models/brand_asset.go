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

// Kept in the database, not on disk, so every control-plane replica serves the same file.
type BrandAsset struct {
	Slot        string `gorm:"primaryKey;size:16"`
	ContentType string `gorm:"size:64;not null"`
	SHA256      string `gorm:"column:sha256;size:64;not null"`
	Size        int    `gorm:"not null"`
	Data        []byte `gorm:"not null"`
	UpdatedAt   time.Time
}
