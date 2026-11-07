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
	"context"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// Severity ranks a preflight finding.
const (
	SeverityBlocker = "blocker" // multi-node cannot work; do not promise it
	SeverityWarning = "warning" // works, but has a sharp edge worth knowing
	SeverityInfo    = "info"
)

// Finding is one thing the operator should know before enabling cluster mode.
type Finding struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

// With any blocked, the swarm forms and DNS resolves while the data plane silently drops every packet.
type FirewallRule struct {
	Port    string `json:"port"`    // "2377/tcp", "esp (ip protocol 50)", …
	Purpose string `json:"purpose"` // what breaks without it
}

type Preflight struct {
	EngineOS string `json:"engine_os"`
	// Single-node cluster mode still works; this is about spanning nodes.
	MultiNodeCapable bool           `json:"multi_node_capable"`
	Findings         []Finding      `json:"findings"`
	Firewall         []FirewallRule `json:"firewall"`
}

// The workspace overlay is encrypted, so ESP (IP protocol 50, not a port) must be allowed too.
func firewallRules() []FirewallRule {
	return []FirewallRule{
		{Port: "2377/tcp", Purpose: "Cluster management (managers only). Without it, nodes cannot join."},
		{Port: "7946/tcp and 7946/udp", Purpose: "Gossip and service discovery. Without it, cross-node DNS does not resolve."},
		{Port: "4789/udp", Purpose: "VXLAN — the overlay data plane. Without it, names resolve and every connection times out."},
		{Port: "esp (IP protocol 50)", Purpose: "IPSec for the encrypted overlay. Not a port — a protocol number, which most firewalls do not open by default. Without it, the symptom is identical to a blocked 4789: DNS works, traffic dies."},
	}
}

// Preflight never mutates anything.
func (s *Service) Preflight(ctx context.Context, clusterID uint) (Preflight, error) {
	mgr, err := s.Manager(ctx, clusterID)
	if err != nil {
		return Preflight{}, err
	}
	info, err := mgr.Info(ctx)
	if err != nil {
		return Preflight{}, err
	}
	p := Preflight{
		EngineOS:         info.OS,
		MultiNodeCapable: true,
		Firewall:         firewallRules(),
	}

	if vm := vmBackedEngine(info); vm != "" {
		p.MultiNodeCapable = false
		p.Findings = append(p.Findings, Finding{
			Severity: SeverityBlocker,
			Title:    fmt.Sprintf("%s cannot carry the overlay data plane to other hosts", vm),
			Detail: "This engine runs Docker inside a Linux VM, and container networking lives inside that VM. " +
				"Swarm's data plane needs VXLAN (4789/udp) delivered to the host interface, and IPSec (IP protocol 50) " +
				"for the encrypted overlay — neither can be forwarded into the VM. " +
				"The swarm will form and cross-node DNS will resolve, and then every connection between nodes will time out. " +
				"Single-node cluster mode works fine. For multi-node, run the manager on a Linux host with a routable address.",
		})
	}

	// A host with no routable address cannot be joined; warn before a cryptic timeout.
	p.Findings = append(p.Findings, Finding{
		Severity: SeverityWarning,
		Title:    "Every swarm node must reach every other node",
		Detail: "Swarm has no NAT traversal: each node dials the others directly on the ports below. " +
			"Nodes behind NAT or CGNAT cannot join a swarm, however they were added to Miabi — " +
			"the outbound agent tunnel does not help here, because Swarm does not use it.",
	})

	return p, nil
}

// These products report "Docker Desktop", "OrbStack" or "Rancher Desktop"; native engines report their distro.
func vmBackedEngine(info docker.Info) string {
	os := strings.ToLower(info.OS)
	switch {
	case strings.Contains(os, "docker desktop"):
		return "Docker Desktop"
	case strings.Contains(os, "orbstack"):
		return "OrbStack"
	case strings.Contains(os, "rancher desktop"):
		return "Rancher Desktop"
	case strings.Contains(os, "darwin"), strings.Contains(os, "windows"):
		// A daemon reporting a non-Linux OS is not running Linux containers natively.
		return "Docker on " + strings.TrimSpace(info.OS)
	}
	return ""
}
