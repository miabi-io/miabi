// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
)

// gatewayConfigState mirrors what the node-gateway handler puts on GatewayStatus for the console's
// encryption badge. Kept as a function so the mapping itself is testable without a live handler.
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

// The badge has to tell the two apart: "encrypted" with a key only this node holds is a different
// guarantee from "encrypted" with a passphrase every gateway shares.
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
