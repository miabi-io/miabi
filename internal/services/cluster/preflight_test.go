// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// A Docker engine running inside a VM cannot carry the overlay data plane to another
// host: VXLAN (4789/udp) is never delivered to the host interface, and ESP (IP
// protocol 50) cannot be forwarded at all — it is not a port. The swarm still forms
// and DNS still resolves, so the failure surfaces much later as an app that resolves
// its database and then times out. Detecting it up front is the whole point.
func TestVMBackedEngineIsDetected(t *testing.T) {
	tests := []struct {
		os   string
		want string
	}{
		{"Docker Desktop", "Docker Desktop"},
		{"Docker Desktop 4.30.0 (149282)", "Docker Desktop"},
		{"OrbStack", "OrbStack"},
		{"Rancher Desktop WSL Distribution", "Rancher Desktop"},
		// Native Linux engines report their distro — these must NOT be flagged, or we
		// would warn every real production install.
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

// ESP is the requirement everyone misses: it is an IP protocol, not a port, so a
// firewall or security group that cheerfully opens 4789/udp still drops every packet.
// The symptom is identical to a blocked VXLAN — DNS resolves, traffic dies — so if we
// omit it from the list, the operator has no way to reach the right answer.
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

// The verdict is the whole product of a net check: three booleans mean nothing to an
// operator, and each failure has a different fix.
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
	// The MTU case is the one that must never be reported as healthy: the connection
	// completes, so every naive check passes, and only the payload reveals it.
	if v := verdict(NetCheckResult{DNS: true, TCP: true}); v == "ok" {
		t.Fatal("a connection that completes but drops a 1400-byte payload was reported as ok")
	}
}
