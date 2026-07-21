// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// FirewallRule is one port (or IP protocol) that must be open between every pair
// of swarm nodes. These are not advisory: with any of them blocked, the swarm forms
// and DNS resolves while the data plane silently drops every packet, so apps
// resolve each other and then time out — the single most confusing failure in
// cluster mode.
type FirewallRule struct {
	Port    string `json:"port"`    // "2377/tcp", "esp (ip protocol 50)", …
	Purpose string `json:"purpose"` // what breaks without it
}

// Preflight is what we can tell an operator about this host before they turn
// cluster mode on.
type Preflight struct {
	// EngineOS is the Docker daemon's reported operating system ("Ubuntu 22.04",
	// "Docker Desktop", "OrbStack", …).
	EngineOS string `json:"engine_os"`
	// MultiNodeCapable is false when this engine cannot carry the overlay data plane
	// to other hosts, whatever the firewall says. Single-node cluster mode still
	// works — this is specifically about spanning nodes.
	MultiNodeCapable bool           `json:"multi_node_capable"`
	Findings         []Finding      `json:"findings"`
	Firewall         []FirewallRule `json:"firewall"`
}

// firewallRules are the ports a swarm needs open between every pair of nodes.
//
// ESP is the one people miss, and it is Miabi's own doing: the workspace overlay is
// created encrypted, so its data plane is IPSec. ESP is IP protocol 50 — not a TCP
// or UDP port — so a firewall or cloud security group that happily opens 4789/udp
// will still drop every packet.
func firewallRules() []FirewallRule {
	return []FirewallRule{
		{Port: "2377/tcp", Purpose: "Cluster management (managers only). Without it, nodes cannot join."},
		{Port: "7946/tcp and 7946/udp", Purpose: "Gossip and service discovery. Without it, cross-node DNS does not resolve."},
		{Port: "4789/udp", Purpose: "VXLAN — the overlay data plane. Without it, names resolve and every connection times out."},
		{Port: "esp (IP protocol 50)", Purpose: "IPSec for the encrypted overlay. Not a port — a protocol number, which most firewalls do not open by default. Without it, the symptom is identical to a blocked 4789: DNS works, traffic dies."},
	}
}

// Preflight inspects the manager's Docker engine and reports what an operator needs
// to know before enabling cluster mode. It never mutates anything, and it works
// whether or not cluster mode is already on.
func (s *Service) Preflight(ctx context.Context) (Preflight, error) {
	info, err := s.clients.Local().Info(ctx)
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

	// A swarm advertises on one address; a host with no routable one cannot be
	// joined. Worth saying before the operator tries and gets a cryptic timeout.
	p.Findings = append(p.Findings, Finding{
		Severity: SeverityWarning,
		Title:    "Every swarm node must reach every other node",
		Detail: "Swarm has no NAT traversal: each node dials the others directly on the ports below. " +
			"Nodes behind NAT or CGNAT cannot join a swarm, however they were added to Miabi — " +
			"the outbound agent tunnel does not help here, because Swarm does not use it.",
	})

	return p, nil
}

// vmBackedEngine names the Docker distribution when the daemon runs inside a Linux
// VM (macOS/Windows), and "" when it is a native Linux engine.
//
// Detection is on the daemon's reported OperatingSystem string, which is what these
// products set: "Docker Desktop", "OrbStack", "Rancher Desktop". A native Linux
// engine reports its distro ("Ubuntu 22.04.4 LTS", "Alpine Linux v3.20", …).
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
