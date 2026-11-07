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

// Keyed by CADirURL, so switching CA registers a distinct account; the key is encrypted, never returned.
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
