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

package handlers

import (
	"strings"

	"github.com/jkaninda/okapi"
)

// Operator-configured through env or the install manifest, so this reports rather than edits.
type NetworkingInfo struct {
	// On an engine below 26 with no ULA prefix Miabi refuses IPv6, so asked-for and active can differ.
	IPv6Enabled   bool   `json:"ipv6_enabled"`
	IPv6Active    bool   `json:"ipv6_active"`
	IPv6ULAPrefix string `json:"ipv6_ula_prefix,omitempty"`
	IPv6Reason    string `json:"ipv6_reason,omitempty"`

	PoolCIDR     string `json:"pool_cidr,omitempty"`
	SubnetPrefix int    `json:"subnet_prefix,omitempty"`
	ProxyNetwork string `json:"proxy_network,omitempty"`
}

// active depends on the engine, so it is resolved at boot and passed in.
func NewNetworkingInfo(info NetworkingInfo) okapi.HandlerFunc {
	return func(c *okapi.Context) error {
		if info.IPv6Enabled && !info.IPv6Active && strings.TrimSpace(info.IPv6Reason) == "" {
			info.IPv6Reason = "this Docker Engine cannot assign an IPv6 range itself; upgrade to Engine 26+ or set MIABI_NETWORK_IPV6_ULA_PREFIX"
		}
		return ok(c, info)
	}
}
