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
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
)

func gatewayConfigState(srv *models.Server, central string) (encrypted bool, scope string) {
	key := edgegateway.ConfigKey(srv, central)
	if key == "" {
		return false, ""
	}
	if key == central {
		return true, "shared"
	}
	return true, "node"
}

// A node-only key and a passphrase every gateway shares are different guarantees.
func TestGatewayConfigBadgeState(t *testing.T) {
	const central = "s3cr3t-key"
	for _, tc := range []struct {
		name      string
		srv       *models.Server
		central   string
		encrypted bool
		scope     string
	}{
		{"a node Miabi deploys", &models.Server{ID: 7, Name: "edge-1"}, central, true, "node"},
		{"the manager's own gateway", &models.Server{ID: 1, IsLocal: true}, central, true, "shared"},
		{"an imported gateway", &models.Server{ID: 9, GatewayImported: true}, central, true, "shared"},
		{"encryption off entirely", &models.Server{ID: 7}, "", false, ""},
	} {
		enc, scope := gatewayConfigState(tc.srv, tc.central)
		if enc != tc.encrypted || scope != tc.scope {
			t.Errorf("%s: (%v, %q), want (%v, %q)", tc.name, enc, scope, tc.encrypted, tc.scope)
		}
	}
}
