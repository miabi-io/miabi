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
	"slices"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/pkg/stack"
)

// ResolveAppNetwork prefers the network found locally so installs from before the rename keep their apps' network.
func ResolveAppNetwork(ctx context.Context, dc docker.Client, configured string) string {
	nets, err := dc.ListNetworks(ctx)
	if err != nil {
		if configured != "" {
			return configured
		}
		// The installs that leave MIABI_PROXY_NETWORK unset are the ones that predate the rename.
		logger.Warn("cannot list docker networks to find the proxy network, assuming the legacy name",
			"network", stack.LegacyNetwork, "error", err)
		return stack.LegacyNetwork
	}
	name := configured
	if name == "" {
		name = discoverAppNetwork(nets)
	}
	if name != stack.LegacyNetwork && slices.ContainsFunc(nets, isLegacyProxyNetwork) {
		logger.Warn("the legacy proxy network still exists but is no longer used: apps attached to it "+
			"are unreachable from the gateway until redeployed. Set MIABI_PROXY_NETWORK to keep using it",
			"legacy", stack.LegacyNetwork, "network", name)
	}
	return name
}

var (
	localAppNetwork *docker.Network
	appNetworkCIDR  string
	appNetworkIPv6  bool
)

// SetAppNetworkAddressing applies miabi.yaml's proxy subnet, which must win over whichever node creates the network first.
func SetAppNetworkAddressing(subnet string, ipv6 bool) {
	appNetworkCIDR, appNetworkIPv6 = subnet, ipv6
}

// AdoptAppNetworkAddressing is the fallback for installs whose manifest predates MIABI_PROXY_SUBNET.
func AdoptAppNetworkAddressing(ctx context.Context, dc docker.Client) {
	if appNetworkCIDR != "" {
		return
	}
	nets, err := dc.ListNetworks(ctx)
	if err != nil {
		logger.Warn("cannot list docker networks; remote nodes will create the proxy network on Docker's default pool",
			"network", AppNetwork, "error", err)
		return
	}
	for i := range nets {
		if nets[i].Name == AppNetwork && (nets[i].Driver == "" || nets[i].Driver == "bridge") {
			n := nets[i]
			localAppNetwork = &n
			return
		}
	}
}

// AppNetworkSpec reuses one subnet everywhere: bridges are node-local, so every node can share it.
func AppNetworkSpec() docker.NetworkSpec {
	spec, _ := appNetworkSpec()
	return spec
}

// defaulted reports the installer's default subnet, which may clash on a host the operator never configured for it.
func appNetworkSpec() (spec docker.NetworkSpec, defaulted bool) {
	spec = docker.NetworkSpec{
		Name:   AppNetwork,
		Driver: "bridge",
		Labels: docker.PlatformLabels(docker.RoleProxyNetwork, docker.ManagedByMiabi, nil),
	}
	ipv6 := appNetworkIPv6
	switch n := localAppNetwork; {
	case appNetworkCIDR != "":
		spec.Subnet, spec.EnableIPv6 = appNetworkCIDR, &ipv6
	case n != nil:
		v6 := n.EnableIPv6
		spec.Subnet, spec.Gateway = n.Subnet, n.Gateway
		spec.EnableIPv6, spec.IPv6Subnet = &v6, n.IPv6Subnet
	default:
		spec.Subnet, spec.EnableIPv6, defaulted = stack.DefaultSubnet, &ipv6, true
	}
	return spec, defaulted
}

func EnsureAppNetwork(ctx context.Context, dc docker.Client) (string, error) {
	spec, defaulted := appNetworkSpec()
	id, err := dc.EnsureNetworkSpec(ctx, spec)
	if err == nil || !defaulted || !strings.Contains(strings.ToLower(err.Error()), "overlap") {
		return id, err
	}
	// Only the unconfigured default yields to Docker's pool; a subnet the operator set is never second-guessed.
	logger.Warn("the default proxy subnet overlaps a network on this host; using Docker's address pool. Set MIABI_PROXY_SUBNET to choose one",
		"network", AppNetwork, "subnet", spec.Subnet, "error", err)
	spec.Subnet = ""
	return dc.EnsureNetworkSpec(ctx, spec)
}

// EnsureNetworkNamed routes the proxy network through AppNetworkSpec so no caller creates it on Docker's pool.
func EnsureNetworkNamed(ctx context.Context, dc docker.Client, name string) (string, error) {
	if name == AppNetwork {
		return EnsureAppNetwork(ctx, dc)
	}
	return dc.EnsureNetwork(ctx, name)
}

func discoverAppNetwork(nets []docker.Network) string {
	var labelled []string
	legacy := false
	for _, n := range nets {
		if role, _ := docker.LabelValue(n.Labels, docker.LabelRole); role == docker.RoleProxyNetwork {
			labelled = append(labelled, n.Name)
		}
		legacy = legacy || n.Name == stack.LegacyNetwork
	}
	switch {
	case len(labelled) == 1:
		return labelled[0]
	case slices.Contains(labelled, stack.DefaultNetwork):
		return stack.DefaultNetwork
	case len(labelled) > 0:
		return labelled[0]
	case legacy:
		return stack.LegacyNetwork
	}
	return stack.DefaultNetwork
}

func isLegacyProxyNetwork(n docker.Network) bool {
	role, _ := docker.LabelValue(n.Labels, docker.LabelRole)
	return n.Name == stack.LegacyNetwork && role == docker.RoleProxyNetwork
}
