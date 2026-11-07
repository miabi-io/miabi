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
	"errors"
	"testing"

	"github.com/miabi-io/miabi/pkg/stack/docker"
)

type driftEngine struct {
	docker.Client
	env []string
	err error
}

func (d driftEngine) InspectContainerConfig(context.Context, string) (docker.ContainerConfig, error) {
	if d.err != nil {
		return docker.ContainerConfig{}, d.err
	}
	return docker.ContainerConfig{Env: d.env}, nil
}

// Node gateways follow the control plane's env, which keeps its old value until that container is recreated.
func TestNodeGatewayImageDrift(t *testing.T) {
	for _, tc := range []struct {
		name string
		eng  driftEngine
		want string
	}{
		{
			"control plane left behind",
			driftEngine{env: []string{"FOO=bar", "MIABI_NODE_GATEWAY_IMAGE=jkaninda/goma-gateway:0.14.0"}},
			"jkaninda/goma-gateway:0.14.0",
		},
		{
			"already in step",
			driftEngine{env: []string{"MIABI_NODE_GATEWAY_IMAGE=jkaninda/goma-gateway:1.0.0"}},
			"",
		},
		{
			"whitespace is not drift",
			driftEngine{env: []string{"MIABI_NODE_GATEWAY_IMAGE= jkaninda/goma-gateway:1.0.0 "}},
			"",
		},
		{
			// The control plane predates the variable, so claiming drift would be a guess.
			"variable absent",
			driftEngine{env: []string{"FOO=bar"}},
			"",
		},
		{
			// This exists to tell an operator something useful, never to fail an upgrade that worked.
			"control plane not inspectable",
			driftEngine{err: errors.New("no such container")},
			"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(tc.eng, func(string, ...any) {}, "")
			if got := s.NodeGatewayImageDrift(context.Background(), "jkaninda/goma-gateway:1.0.0"); got != tc.want {
				t.Errorf("drift = %q, want %q", got, tc.want)
			}
		})
	}
}
