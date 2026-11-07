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

// DatabaseSize is a named CPU/memory limit class (Enterprise); edits apply only to instances sized afterwards.
type DatabaseSize struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// Name is the handle manifests and the API use. Fixed once created, since instances carry it as a label.
	Name        string    `json:"name" gorm:"uniqueIndex;not null"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	MemoryBytes int64     `json:"memory_bytes" gorm:"not null"`
	NanoCPUs    int64     `json:"nano_cpus" gorm:"not null"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ValidDatabaseSizeName reports whether n is a usable size name: a short lowercase slug.
func ValidDatabaseSizeName(n string) bool { return poolNamePattern.MatchString(n) }
