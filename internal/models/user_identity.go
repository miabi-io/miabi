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

// UserIdentity: SSO logins match on the provider's stable subject, never an email users may change.
type UserIdentity struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	ProviderID uint      `json:"provider_id" gorm:"not null;uniqueIndex:idx_identity_subject;uniqueIndex:idx_identity_user"`
	Subject    string    `json:"subject" gorm:"not null;uniqueIndex:idx_identity_subject"`
	UserID     uint      `json:"user_id" gorm:"not null;index;uniqueIndex:idx_identity_user"`
	CreatedAt  time.Time `json:"created_at"`
}
