// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// envValue pulls one KEY=value out of a rendered env slice.
func envValue(env []string, key string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v
		}
	}
	return ""
}

const central = "s3cr3t-key"

// The point of the change: one shared passphrase meant any gateway could decrypt any other's
// config. Each node's must differ from the central one and from every other node's.
func TestConfigKeyIsPerNode(t *testing.T) {
	a := ConfigKey(&models.Server{ID: 7, Name: "edge-a"}, central)
	b := ConfigKey(&models.Server{ID: 8, Name: "edge-b"}, central)

	if a == "" || b == "" {
		t.Fatal("no key derived for a node")
	}
	if a == central || b == central {
		t.Fatal("a node was handed the central passphrase")
	}
	if a == b {
		t.Fatal("two nodes derived the same key")
	}
	// Deterministic: the container env and the provider's rendering are computed separately and
	// must agree, with nothing stored to tie them together.
	if ConfigKey(&models.Server{ID: 7, Name: "edge-a"}, central) != a {
		t.Error("the derivation is not stable")
	}
	// Keyed by ID, not name — renaming a node must not re-key it and strand its gateway.
	if ConfigKey(&models.Server{ID: 7, Name: "renamed"}, central) != a {
		t.Error("renaming a node changed its key")
	}
}

// The manager's gateway ships with the control plane and its environment is the operator's, so it
// keeps the central passphrase. An imported gateway is never redeployed, so Miabi could not hand it
// a derived key even if it wanted to.
func TestConfigKeyKeepsTheCentralPassphraseWhereItMust(t *testing.T) {
	for _, tc := range []struct {
		name string
		srv  *models.Server
	}{
		{"the manager", &models.Server{ID: 1, Name: "manager", IsLocal: true}},
		{"an imported gateway", &models.Server{ID: 9, Name: "edge-x", GatewayImported: true}},
		{"no server at all", nil},
	} {
		if got := ConfigKey(tc.srv, central); got != central {
			t.Errorf("%s: key = %q, want the central passphrase", tc.name, got)
		}
	}
}

// Encryption is one install-wide switch. With no central key configured nothing is encrypted, so a
// node must not be handed a key for content that has none — its gateway would look for encrypted
// fields that are not there.
func TestConfigKeyOffWhenEncryptionIsOff(t *testing.T) {
	if got := ConfigKey(&models.Server{ID: 7}, ""); got != "" {
		t.Errorf("key = %q, want none when config encryption is off", got)
	}
}
