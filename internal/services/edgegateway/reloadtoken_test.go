// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// The reload call carries its bearer over plain HTTP on the node's own network. Sending the gateway
// token there would put the provider credential — which fetches a config full of decrypted
// middleware secrets — in front of anyone on that path.
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
	// Deterministic, because the control plane derives it at call time and the gateway was given it
	// at deploy: the two must agree without a second secret being stored anywhere.
	if ReloadToken(gw) != reload {
		t.Error("the derivation is not stable, so the call and the container would disagree")
	}
	// Different nodes must not share one.
	if ReloadToken("mb_node_other") == reload {
		t.Error("two nodes derived the same reload token")
	}
}

// What the container is given and what Miabi sends must be the same value, or every on-demand
// reload 401s and nodes silently fall back to the poll interval.
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
