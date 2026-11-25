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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/pkg/stack"
)

type netLister struct {
	docker.Client
	nets []docker.Network
	err  error
}

func (n netLister) ListNetworks(context.Context) ([]docker.Network, error) { return n.nets, n.err }

func proxyNet(name string) docker.Network {
	return docker.Network{Name: name, Labels: map[string]string{docker.LabelRole: docker.RoleProxyNetwork}}
}

func TestResolveAppNetwork(t *testing.T) {
	plain := func(name string) docker.Network { return docker.Network{Name: name} }
	internal := docker.Network{Name: "miabi-internal", Labels: map[string]string{docker.LabelRole: docker.RolePlatformInternal}}

	for _, tc := range []struct {
		name       string
		configured string
		dc         netLister
		want       string
	}{
		{"configured wins over discovery", "custom", netLister{nets: []docker.Network{proxyNet("other")}}, "custom"},
		{"configured survives a list error", "custom", netLister{err: errors.New("down")}, "custom"},
		{"unset with a list error assumes a pre-rename install", "", netLister{err: errors.New("down")}, stack.LegacyNetwork},
		{"fresh engine gets the new default", "", netLister{nets: []docker.Network{plain("bridge"), internal}}, stack.DefaultNetwork},
		{"labelled legacy network is kept", "", netLister{nets: []docker.Network{proxyNet(stack.LegacyNetwork), internal}}, stack.LegacyNetwork},
		{"unlabelled legacy network from a manual install is kept", "", netLister{nets: []docker.Network{plain(stack.LegacyNetwork)}}, stack.LegacyNetwork},
		{"a renamed labelled network is found by label", "", netLister{nets: []docker.Network{proxyNet("edge"), plain(stack.LegacyNetwork)}}, "edge"},
		{"both labelled prefers the new default", "", netLister{nets: []docker.Network{proxyNet(stack.LegacyNetwork), proxyNet(stack.DefaultNetwork)}}, stack.DefaultNetwork},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveAppNetwork(context.Background(), tc.dc, tc.configured); got != tc.want {
				t.Fatalf("ResolveAppNetwork = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAppNetworkSpecMirrorsLocalAddressing(t *testing.T) {
	prevName, prevLocal, prevCIDR := AppNetwork, localAppNetwork, appNetworkCIDR
	t.Cleanup(func() { AppNetwork, localAppNetwork, appNetworkCIDR = prevName, prevLocal, prevCIDR })

	AppNetwork, localAppNetwork, appNetworkCIDR = "miabi", nil, ""
	if spec := AppNetworkSpec(); spec.Subnet != stack.DefaultSubnet {
		t.Fatalf("unconfigured spec subnet = %q, want the installer default %s", spec.Subnet, stack.DefaultSubnet)
	}

	AdoptAppNetworkAddressing(context.Background(), netLister{nets: []docker.Network{
		{Name: "miabi-ingress", Driver: "overlay", Subnet: "10.0.1.0/24"},
		{Name: "miabi", Driver: "bridge", Subnet: "10.63.0.0/16", Gateway: "10.63.0.1", EnableIPv6: true, IPv6Subnet: "fdbd::/64"},
	}})
	spec := AppNetworkSpec()
	if spec.Name != "miabi" || spec.Driver != "bridge" || spec.Subnet != "10.63.0.0/16" || spec.Gateway != "10.63.0.1" {
		t.Fatalf("spec = %+v, want the local bridge's IPv4 addressing", spec)
	}
	if spec.EnableIPv6 == nil || !*spec.EnableIPv6 || spec.IPv6Subnet != "fdbd::/64" {
		t.Fatalf("spec IPv6 = %v %q, want enabled fdbd::/64", spec.EnableIPv6, spec.IPv6Subnet)
	}
	if role, _ := docker.LabelValue(spec.Labels, docker.LabelRole); role != docker.RoleProxyNetwork {
		t.Fatalf("role label = %q, want %q", role, docker.RoleProxyNetwork)
	}
}

func TestAppNetworkSpecPrefersConfiguredSubnet(t *testing.T) {
	prevName, prevLocal, prevCIDR, prevV6 := AppNetwork, localAppNetwork, appNetworkCIDR, appNetworkIPv6
	t.Cleanup(func() {
		AppNetwork, localAppNetwork, appNetworkCIDR, appNetworkIPv6 = prevName, prevLocal, prevCIDR, prevV6
	})

	AppNetwork, localAppNetwork = "miabi-proxy", nil
	SetAppNetworkAddressing("10.63.0.0/16", true)
	AdoptAppNetworkAddressing(context.Background(), netLister{nets: []docker.Network{
		{Name: "miabi-proxy", Driver: "bridge", Subnet: "172.18.0.0/16"},
	}})
	spec := AppNetworkSpec()
	if spec.Subnet != "10.63.0.0/16" || spec.EnableIPv6 == nil || !*spec.EnableIPv6 {
		t.Fatalf("spec = %+v, want the configured 10.63.0.0/16 with IPv6", spec)
	}
}

type overlapEnsurer struct {
	docker.Client
	overlap string
	specs   []docker.NetworkSpec
}

func (o *overlapEnsurer) EnsureNetworkSpec(_ context.Context, spec docker.NetworkSpec) (string, error) {
	o.specs = append(o.specs, spec)
	if spec.Subnet != "" && spec.Subnet == o.overlap {
		return "", errors.New("Pool overlaps with other one on this address space")
	}
	return "id", nil
}

// Only the unconfigured default yields to Docker's pool when it clashes; an operator's subnet must fail loudly.
func TestDefaultProxySubnetFallsBackOnOverlap(t *testing.T) {
	prevName, prevLocal, prevCIDR := AppNetwork, localAppNetwork, appNetworkCIDR
	t.Cleanup(func() { AppNetwork, localAppNetwork, appNetworkCIDR = prevName, prevLocal, prevCIDR })
	AppNetwork, localAppNetwork, appNetworkCIDR = "miabi-proxy", nil, ""

	dc := &overlapEnsurer{overlap: stack.DefaultSubnet}
	if _, err := EnsureAppNetwork(context.Background(), dc); err != nil {
		t.Fatalf("default subnet overlap: %v", err)
	}
	if len(dc.specs) != 2 || dc.specs[1].Subnet != "" {
		t.Fatalf("specs = %+v, want a retry on Docker's pool", dc.specs)
	}

	appNetworkCIDR = "10.99.0.0/16"
	dc = &overlapEnsurer{overlap: "10.99.0.0/16"}
	if _, err := EnsureAppNetwork(context.Background(), dc); err == nil || len(dc.specs) != 1 {
		t.Fatalf("configured subnet overlap = %v after %d attempts, want the error and no retry", err, len(dc.specs))
	}
}
