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

package cluster

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// A VM-backed engine never delivers VXLAN or ESP to the host, so failures surface later as timeouts.
func TestVMBackedEngineIsDetected(t *testing.T) {
	tests := []struct {
		os   string
		want string
	}{
		{"Docker Desktop", "Docker Desktop"},
		{"Docker Desktop 4.30.0 (149282)", "Docker Desktop"},
		{"OrbStack", "OrbStack"},
		{"Rancher Desktop WSL Distribution", "Rancher Desktop"},
		// Flagging these would warn every real production install.
		{"Ubuntu 22.04.4 LTS", ""},
		{"Alpine Linux v3.20", ""},
		{"Debian GNU/Linux 12 (bookworm)", ""},
		{"CentOS Stream 9", ""},
	}
	for _, tt := range tests {
		t.Run(tt.os, func(t *testing.T) {
			got := vmBackedEngine(docker.Info{OS: tt.os})
			if tt.want == "" && got != "" {
				t.Fatalf("native Linux engine %q was flagged as VM-backed (%q)", tt.os, got)
			}
			if tt.want != "" && got != tt.want {
				t.Fatalf("vmBackedEngine(%q) = %q, want %q", tt.os, got, tt.want)
			}
		})
	}
}

// ESP is an IP protocol, not a port, and its symptom is identical to blocked VXLAN.
func TestFirewallRulesNameESPAndVXLAN(t *testing.T) {
	var joined string
	for _, r := range firewallRules() {
		joined += r.Port + " " + r.Purpose + "\n"
	}
	for _, want := range []string{"2377/tcp", "7946", "4789/udp", "esp"} {
		if !strings.Contains(strings.ToLower(joined), want) {
			t.Errorf("firewall rules do not mention %q — an operator cannot fix what we never named", want)
		}
	}
	if !strings.Contains(strings.ToLower(joined), "protocol 50") {
		t.Error("ESP is listed but not identified as IP protocol 50; " +
			"an operator will look for a port and not find one")
	}
}

// Each failure has a different fix, so the verdict must name it.
func TestNetCheckVerdicts(t *testing.T) {
	tests := []struct {
		name string
		r    NetCheckResult
		want string // substring the verdict must carry
	}{
		{"healthy path", NetCheckResult{DNS: true, TCP: true, Payload: true}, "ok"},
		{"MTU black hole", NetCheckResult{DNS: true, TCP: true}, "MTU"},
		{"data plane blocked", NetCheckResult{DNS: true}, "4789"},
		{"gossip blocked", NetCheckResult{}, "7946"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verdict(tt.r)
			if !strings.Contains(got, tt.want) {
				t.Errorf("verdict = %q, want it to mention %q", got, tt.want)
			}
		})
	}
	// The connection completes, so every naive check passes; only the payload reveals MTU trouble.
	if v := verdict(NetCheckResult{DNS: true, TCP: true}); v == "ok" {
		t.Fatal("a connection that completes but drops a 1400-byte payload was reported as ok")
	}
}
