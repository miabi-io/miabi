//go:build dockere2e

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Behind a build tag: needs a live daemon. Proves ListNetworks reports both address families and
// their gateways, which is what the network detail modal renders.
//
//	go test -tags dockere2e ./internal/docker/ -run NetworkAddressing -v
func TestNetworkAddressingOnALiveDaemon(t *testing.T) {
	cli, err := New()
	if err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	name := fmt.Sprintf("mb-ipam-e2e-%d", time.Now().UnixNano())
	yes := true
	// 10.199.88.0/24 sits outside Miabi's pool, so this cannot collide with a live network.
	spec := NetworkSpec{
		Name:       name,
		Subnet:     "10.199.88.0/24",
		EnableIPv6: &yes,
		IPv6Subnet: "fd42:6d69:6162:c758::/64",
	}
	if _, err := cli.CreateNetworkSpec(ctx, spec); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = cli.RemoveNetwork(context.Background(), name) }()

	nets, err := cli.ListNetworks(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got Network
	for _, n := range nets {
		if n.Name == name {
			got = n
		}
	}
	if got.Name == "" {
		t.Fatal("the network was not listed")
	}
	if got.Subnet != "10.199.88.0/24" {
		t.Errorf("IPv4 subnet = %q", got.Subnet)
	}
	if got.IPv6Subnet != "fd42:6d69:6162:c758::/64" {
		t.Errorf("IPv6 subnet = %q — the v6 IPAM entry was not picked up", got.IPv6Subnet)
	}
	if !got.EnableIPv6 {
		t.Error("EnableIPv6 was not reported")
	}
	// Docker fills in a gateway per family even when the spec does not ask for one.
	if got.Gateway == "" || got.IPv6Gateway == "" {
		t.Errorf("gateways = %q / %q, want both reported", got.Gateway, got.IPv6Gateway)
	}
	t.Logf("v4 %s gw %s · v6 %s gw %s", got.Subnet, got.Gateway, got.IPv6Subnet, got.IPv6Gateway)
}
