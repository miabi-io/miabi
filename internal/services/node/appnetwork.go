// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package node

import (
	"context"
	"slices"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/pkg/stack"
)

// ResolveAppNetwork returns the proxy network name: configured when set, otherwise the one found on
// the local engine, so an install created before the rename keeps the network its apps are attached
// to. Only an engine with no proxy network at all gets stack.DefaultNetwork.
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
