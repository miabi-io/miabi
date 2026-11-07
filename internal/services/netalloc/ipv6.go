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

package netalloc

import (
	"fmt"
	"net/netip"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
)

// DeriveIPv6 derives a stable /64 from the IPv4 subnet (works on Engine 25); an empty prefix returns "" (daemon chooses).
func DeriveIPv6(ulaPrefix, ipv4Subnet string) (string, error) {
	if ulaPrefix == "" {
		return "", nil
	}
	base, err := netip.ParsePrefix(ulaPrefix)
	if err != nil {
		return "", fmt.Errorf("invalid IPv6 ULA prefix %q: %w", ulaPrefix, err)
	}
	if !base.Addr().Is6() {
		return "", fmt.Errorf("IPv6 ULA prefix %q is not an IPv6 prefix", ulaPrefix)
	}
	// A /48 leaves exactly the 16 subnet-id bits this mapping needs; anything longer cannot hold them.
	if base.Bits() > 48 {
		return "", fmt.Errorf("IPv6 ULA prefix %q must be /48 or shorter to carry a /64 per network", ulaPrefix)
	}
	v4, err := netip.ParsePrefix(ipv4Subnet)
	if err != nil {
		return "", fmt.Errorf("invalid IPv4 subnet %q: %w", ipv4Subnet, err)
	}
	if !v4.Addr().Is4() {
		return "", fmt.Errorf("subnet %q is not IPv4", ipv4Subnet)
	}
	octets := v4.Addr().As4()
	addr := base.Addr().As16()
	// Bytes 6 and 7 are the /64's subnet id, immediately after a /48 prefix.
	addr[6] = octets[1]
	addr[7] = octets[2]
	return netip.PrefixFrom(netip.AddrFrom16(addr), 64).String(), nil
}

func (s *Service) SetIPv6(enabled bool, ulaPrefix string) {
	s.ipv6Enabled = enabled
	s.ipv6ULAPrefix = ulaPrefix
}

// Set explicitly either way: inheriting the daemon default is how IPv6 ended up latent before.
func (s *Service) applyIPv6(spec *docker.NetworkSpec) {
	enabled := s.ipv6Enabled
	spec.EnableIPv6 = &enabled
	if !enabled || spec.Subnet == "" {
		return
	}
	v6, err := DeriveIPv6(s.ipv6ULAPrefix, spec.Subnet)
	if err != nil {
		// A bad prefix is a configuration mistake, not a reason to stop deploying.
		logger.Warn("could not derive an IPv6 subnet; letting the daemon assign one",
			"network", spec.Name, "ipv4", spec.Subnet, "error", err)
		return
	}
	spec.IPv6Subnet = v6
}
