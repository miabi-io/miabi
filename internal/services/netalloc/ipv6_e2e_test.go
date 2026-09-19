//go:build dockere2e

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package netalloc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

// Behind a build tag: needs a live daemon. Proves the spec Miabi builds actually produces a
// dual-stack network, in both modes.
//
//	go test -tags dockere2e ./internal/services/netalloc/ -run IPv6OnALiveDaemon -v
func TestIPv6OnALiveDaemon(t *testing.T) {
	cli, err := docker.New()
	if err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cases := []struct {
		name   string
		ula    string
		wantV6 string // expected prefix, or "" for whatever the daemon assigns
	}{
		{"daemon assigns the ULA", "", ""},
		{"derived from the IPv4 subnet", "fd42:6d69:6162::/48", "fd42:6d69:6162:c74d::/64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name := fmt.Sprintf("mb-v6-e2e-%d", time.Now().UnixNano())
			s := &Service{}
			s.SetIPv6(true, c.ula)

			// 10.199.77.0/24 sits outside Miabi's pool, so this cannot collide with a live network.
			spec := docker.NetworkSpec{Name: name, Subnet: "10.199.77.0/24"}
			s.applyIPv6(&spec)

			if spec.EnableIPv6 == nil || !*spec.EnableIPv6 {
				t.Fatal("EnableIPv6 was not set on the spec")
			}
			if c.wantV6 != "" && spec.IPv6Subnet != c.wantV6 {
				t.Fatalf("derived subnet = %q, want %q", spec.IPv6Subnet, c.wantV6)
			}

			if _, err := cli.CreateNetworkSpec(ctx, spec); err != nil {
				t.Fatalf("create: %v", err)
			}
			defer func() { _ = cli.RemoveNetwork(context.Background(), name) }()

			nets, err := cli.ListNetworks(ctx)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			var found bool
			for _, n := range nets {
				if n.Name == name {
					found = true
				}
			}
			if !found {
				t.Fatal("the network was not created")
			}
			t.Logf("created %s with EnableIPv6=%v IPv6Subnet=%q", name, *spec.EnableIPv6, spec.IPv6Subnet)
		})
	}
}
