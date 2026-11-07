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

// WorkspaceSealingKey keeps older versions so sealed values already in git keep opening.
type WorkspaceSealingKey struct {
	ID          uint `json:"-" gorm:"primaryKey"`
	WorkspaceID uint `json:"-" gorm:"index:idx_wsseal_ws_ver,unique;not null"`
	Version     int  `json:"version" gorm:"index:idx_wsseal_ws_ver,unique;not null"`
	// PublicKey is the age recipient ("age1…") that clients seal to.
	PublicKey string `json:"public_key" gorm:"type:text;not null"`
	// Wrapped under the KEK, not the DEK, so DEK rotation never touches it and a DR restore brings it back.
	IdentityEnc string    `json:"-" gorm:"type:text;not null"`
	Active      bool      `json:"active" gorm:"not null;default:true;index"`
	CreatedAt   time.Time `json:"created_at"`
}
