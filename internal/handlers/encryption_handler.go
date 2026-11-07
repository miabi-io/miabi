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

package handlers

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// Informational only: per-workspace keys and auto-rotation are operator-configured via env.
type EncryptionInfo struct {
	EncryptionEnabled       bool `json:"encryption_enabled"` // a master key is configured
	PerWorkspaceKeys        bool `json:"per_workspace_keys"` // the keyring is wired
	AutoRotate              bool `json:"auto_rotate"`
	RotateMonths            int  `json:"rotate_months"`
	GatewayConfigEncryption bool `json:"gateway_config_encryption"`
}

// NewEncryptionInfo returns an admin handler reporting the encryption posture.
func NewEncryptionInfo(autoRotate bool, rotateMonths int, gatewayConfigEncryption bool) okapi.HandlerFunc {
	return func(c *okapi.Context) error {
		return ok(c, EncryptionInfo{
			EncryptionEnabled:       crypto.Enabled(),
			PerWorkspaceKeys:        crypto.CurrentKeyring() != nil,
			AutoRotate:              autoRotate,
			RotateMonths:            rotateMonths,
			GatewayConfigEncryption: gatewayConfigEncryption,
		})
	}
}
