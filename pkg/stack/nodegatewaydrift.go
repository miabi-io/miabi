// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"context"
	"strings"
)

// nodeGatewayImageEnv is the variable the control plane reads the image for node gateways from.
// It is set on the CONTROL PLANE, not the gateway — see the spec's env block.
const nodeGatewayImageEnv = "MIABI_NODE_GATEWAY_IMAGE"

// NodeGatewayImageDrift reports the image the running control plane is still handing to node
// gateways when that differs from want, or "" when they agree.
//
// They can differ because the two live in different containers: upgrading the gateway component
// rewrites the manifest and recreates the gateway, while the value nodes actually follow rides the
// control plane's environment and is only re-rendered when THAT container is recreated. Until then
// every node keeps provisioning the version just upgraded away from.
//
// A control plane that cannot be inspected reports no drift: this exists to tell an operator
// something useful, never to fail an upgrade that otherwise worked.
func (s *Service) NodeGatewayImageDrift(ctx context.Context, want string) string {
	cfg, err := s.dc.InspectContainerConfig(ctx, ContainerControlPlane)
	if err != nil {
		return ""
	}
	for _, e := range cfg.Env {
		v, ok := strings.CutPrefix(e, nodeGatewayImageEnv+"=")
		if !ok {
			continue
		}
		if strings.TrimSpace(v) == strings.TrimSpace(want) {
			return ""
		}
		return v
	}
	return ""
}
