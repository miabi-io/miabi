// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package netalloc

import (
	"fmt"
	"net/netip"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
)

// DeriveIPv6 maps an allocated IPv4 subnet onto a /64 under a ULA prefix, so the two address spaces
// line up: 10.64.5.0/24 under fd42:6d69:6162::/48 becomes fd42:6d69:6162:4005::/64, and an operator
// reading a v6 address can see which v4 network it belongs to.
//
// Deriving rather than letting the daemon assign buys two things: it works on Docker Engine 25, which
// cannot pick a ULA itself, and the addresses are stable and predictable — the same network gets the
// same /64 on every node and after every recreate, which is what makes a firewall rule possible to
// write.
//
// The subnet id is the middle 16 bits of the IPv4 network address (its second and third octets).
// Across the default pool — 10.64.0.0/12 carved into /24s — those bits are unique per subnet, so no
// two networks collide. An empty prefix means "let the daemon choose" and returns "".
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

// SetIPv6 records the install's IPv6 policy: whether Miabi-created networks are dual-stack, and
// whether their /64 is derived from the IPv4 subnet under a ULA prefix rather than chosen by the
// daemon.
func (s *Service) SetIPv6(enabled bool, ulaPrefix string) {
	s.ipv6Enabled = enabled
	s.ipv6ULAPrefix = ulaPrefix
}

// applyIPv6 stamps the v6 policy onto a spec whose IPv4 subnet has just been decided. It is set
// EXPLICITLY either way: a network that inherits the daemon's default follows a setting nobody made
// here, which is how IPv6 ended up latent in the first place.
func (s *Service) applyIPv6(spec *docker.NetworkSpec) {
	enabled := s.ipv6Enabled
	spec.EnableIPv6 = &enabled
	if !enabled || spec.Subnet == "" {
		return
	}
	v6, err := DeriveIPv6(s.ipv6ULAPrefix, spec.Subnet)
	if err != nil {
		// Fall back to the daemon's own assignment rather than failing the network: a bad prefix is
		// a configuration mistake, not a reason to stop deploying.
		logger.Warn("could not derive an IPv6 subnet; letting the daemon assign one",
			"network", spec.Name, "ipv4", spec.Subnet, "error", err)
		return
	}
	spec.IPv6Subnet = v6
}
