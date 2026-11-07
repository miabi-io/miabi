// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"context"
	"strings"
)

// Set on the control plane, not the gateway.
const nodeGatewayImageEnv = "MIABI_NODE_GATEWAY_IMAGE"

// The value rides the control plane's env and is re-rendered only when that container is recreated.
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
