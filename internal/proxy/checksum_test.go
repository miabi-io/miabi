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

package proxy

import (
	"bytes"
	"strings"
	"testing"
)

// Encryption is randomised, so a checksum of the body would change on every poll and defeat If-None-Match.
func TestBundleChecksumIsStableAcrossRandomisedEncryption(t *testing.T) {
	routes := []RenderedRoute{{WorkspaceID: 1, Name: "web", Hosts: []string{"a.example.com"},
		Certs: []CertPair{{CertPEM: "cert", KeyPEM: "key"}}}}
	mws := []RenderedMiddleware{{WorkspaceID: 1, Name: "auth", Type: "basic", Rule: map[string]interface{}{"users": []string{"u:p"}}}}

	a, sumA, err := RenderProviderBundle(routes, mws, "k1")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	b, sumB, _ := RenderProviderBundle(routes, mws, "k1")
	if bytes.Equal(a, b) {
		t.Fatal("encrypted bodies are identical, so this test no longer covers randomised encryption")
	}
	if sumA == "" || sumA != sumB {
		t.Fatalf("checksums %q and %q differ for the same plaintext", sumA, sumB)
	}
	if !strings.Contains(string(a), "checksum: "+sumA) {
		t.Errorf("rendered bundle does not carry its checksum:\n%s", a)
	}
	if BundleChecksum(routes, mws, "k2") == sumA {
		t.Error("a different key kept the same checksum, so a re-keyed gateway would be told nothing changed")
	}
	changed := []RenderedRoute{{WorkspaceID: 1, Name: "web", Hosts: []string{"b.example.com"}}}
	if BundleChecksum(changed, mws, "k1") == sumA {
		t.Error("a route change kept the same checksum")
	}
}
