// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// ACMEAccount is the platform's registered ACME account for a CA directory, reused across
// all managed-certificate issuance. The account key is encrypted at rest and never returned.
// Keyed by CADirURL, so switching CA (staging -> production) registers a distinct account.
type ACMEAccount struct {
	ID       uint   `json:"id" gorm:"primaryKey"`
	CADirURL string `json:"ca_dir_url" gorm:"uniqueIndex;not null"`
	Email    string `json:"email" gorm:"not null"`
	// KeyEnc is the PEM-encoded account private key, crypto.Encrypt'd at rest.
	KeyEnc string `json:"-" gorm:"type:text;not null"`
	// RegistrationJSON is the lego registration.Resource (account URL + body).
	RegistrationJSON string `json:"-" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
