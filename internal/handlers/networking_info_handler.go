// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strings"

	"github.com/jkaninda/okapi"
)

// NetworkingInfo is the platform networking posture shown read-only in admin Settings. Every field
// is operator-configured through the environment or the install manifest, so this reports rather
// than edits.
type NetworkingInfo struct {
	// IPv6Enabled is what the operator ASKED for (MIABI_NETWORK_IPV6). IPv6Active is whether Miabi
	// actually enables it: on an engine below 26 with no ULA prefix, it refuses rather than making
	// every network create fail, and the two answers differ.
	IPv6Enabled bool `json:"ipv6_enabled"`
	IPv6Active  bool `json:"ipv6_active"`
	// IPv6ULAPrefix is set when each network's /64 is derived from its IPv4 subnet rather than
	// chosen by the daemon.
	IPv6ULAPrefix string `json:"ipv6_ula_prefix,omitempty"`
	// IPv6Reason explains a difference between asked-for and active, so the page says why instead of
	// showing a switch that looks stuck.
	IPv6Reason string `json:"ipv6_reason,omitempty"`

	PoolCIDR     string `json:"pool_cidr,omitempty"`
	SubnetPrefix int    `json:"subnet_prefix,omitempty"`
	ProxyNetwork string `json:"proxy_network,omitempty"`
}

// NewNetworkingInfo returns an admin handler reporting the networking posture. active is resolved at
// boot (it depends on the engine), so it is passed in rather than re-derived here.
func NewNetworkingInfo(info NetworkingInfo) okapi.HandlerFunc {
	return func(c *okapi.Context) error {
		if info.IPv6Enabled && !info.IPv6Active && strings.TrimSpace(info.IPv6Reason) == "" {
			info.IPv6Reason = "this Docker Engine cannot assign an IPv6 range itself; upgrade to Engine 26+ or set MIABI_NETWORK_IPV6_ULA_PREFIX"
		}
		return ok(c, info)
	}
}
