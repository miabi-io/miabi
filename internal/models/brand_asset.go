// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// BrandAsset is an image the operator uploaded for the brand: a logo or the favicon.
// Kept in the database rather than on disk, so every control-plane replica serves
// the same file.
type BrandAsset struct {
	Slot        string `gorm:"primaryKey;size:16"`
	ContentType string `gorm:"size:64;not null"`
	SHA256      string `gorm:"column:sha256;size:64;not null"`
	Size        int    `gorm:"not null"`
	Data        []byte `gorm:"not null"`
	UpdatedAt   time.Time
}
