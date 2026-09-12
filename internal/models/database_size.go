// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// DatabaseSize is a named CPU and memory limit a database instance can be given, like a cloud instance class
// (Enterprise). Editing one sizes only the instances given it afterwards; those already on it keep their limits.
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
