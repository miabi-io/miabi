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

package edgegateway

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// The gateway token fetches decrypted middleware secrets, so it must not ride the plaintext reload call.
func TestReloadTokenIsNotTheGatewayToken(t *testing.T) {
	const gw = "mb_node_7f3a9c2e1d4b"

	reload := ReloadToken(gw)
	if reload == "" {
		t.Fatal("no reload token derived")
	}
	if reload == gw {
		t.Fatal("the reload token IS the gateway token — the provider credential still rides the wire")
	}
	if strings.Contains(reload, gw) {
		t.Error("the reload token embeds the gateway token")
	}
	// The control plane derives it at call time and the gateway got it at deploy, so it must be stable.
	if ReloadToken(gw) != reload {
		t.Error("the derivation is not stable, so the call and the container would disagree")
	}
	// Different nodes must not share one.
	if ReloadToken("mb_node_other") == reload {
		t.Error("two nodes derived the same reload token")
	}
}

// A mismatch makes every reload 401 and nodes silently fall back to polling.
func TestGatewayEnvCarriesTheDerivedReloadToken(t *testing.T) {
	const gw = "mb_node_7f3a9c2e1d4b"
	s := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")

	var got string
	for _, e := range s.gatewayEnv(&models.Server{Name: "edge-1", Address: "10.0.0.5"}, gw, "") {
		if v, ok := strings.CutPrefix(e, reloadTokenEnv+"="); ok {
			got = v
		}
	}
	if got == "" {
		t.Fatalf("no %s in the gateway env", reloadTokenEnv)
	}
	if got == gw {
		t.Fatal("the container was handed the gateway token as its reload token")
	}
	if got != ReloadToken(gw) {
		t.Errorf("%s = %q, want the derived reload token — Miabi sends that one", reloadTokenEnv, got)
	}
}
