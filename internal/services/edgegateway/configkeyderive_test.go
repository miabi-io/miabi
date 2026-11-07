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

// A shared passphrase let any gateway decrypt another's config.
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
	// The container env and the provider's rendering compute it separately and must agree.
	if ConfigKey(&models.Server{ID: 7, Name: "edge-a"}, central) != a {
		t.Error("the derivation is not stable")
	}
	// Keyed by ID, not name — renaming a node must not re-key it and strand its gateway.
	if ConfigKey(&models.Server{ID: 7, Name: "renamed"}, central) != a {
		t.Error("renaming a node changed its key")
	}
}

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

func TestConfigKeyOffWhenEncryptionIsOff(t *testing.T) {
	if got := ConfigKey(&models.Server{ID: 7}, ""); got != "" {
		t.Errorf("key = %q, want none when config encryption is off", got)
	}
}
