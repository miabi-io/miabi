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
	"fmt"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/pkg/stack/docker"
)

// Component is a running piece of the platform stack, as found on the engine.
type Component struct {
	Name      string
	Role      string
	State     string // running, exited, …
	Health    string // healthy, unhealthy, starting, "" (no healthcheck)
	Image     string
	ManagedBy string // compose | miabi | external | "" (an install predating labels)
	Status    string
}

// Role, not part-of membership, decides: registry, agents and edge gateways are Miabi but not the stack.
var stackRoles = map[string]bool{
	docker.RoleControlPlane:  true,
	docker.RolePlatformDB:    true,
	docker.RolePlatformCache: true,
	docker.RoleGateway:       true,
}

// Fallback for pre-label stacks; only helps CLI-shaped installs since Compose names are <project>-<service>-<n>.
var stackNames = map[string]int{
	ContainerControlPlane: 0,
	ContainerPostgres:     1,
	ContainerRedis:        2,
	ContainerGateway:      3,
}

// By role, so `miabi stack status` describes a Compose install as well as a CLI one.
func (s *Service) Discover(ctx context.Context) ([]Component, error) {
	all, err := s.dc.ListContainers(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	var out []Component
	for _, c := range all {
		name := containerName(c)
		role, _ := docker.LabelValue(c.Labels, docker.LabelRole)

		isStack := docker.IsPlatformStack(c.Labels) && stackRoles[role]
		if !isStack {
			// Unlabeled, but named like the stack: a pre-Phase-1 install.
			if _, known := stackNames[name]; !known || docker.IsManaged(c.Labels) {
				continue
			}
		}
		// A rollout's throwaway container is not a component of the stack.
		if strings.HasSuffix(name, "-test") {
			continue
		}

		// Docker's list API has no health field, so health comes from an inspect; at most four containers.
		health := c.Health
		if full, ierr := s.dc.InspectContainer(ctx, c.ID); ierr == nil {
			health = full.Health
		}

		out = append(out, Component{
			Name:      name,
			Role:      role,
			State:     c.State,
			Health:    health,
			Image:     c.Image,
			ManagedBy: docker.ManagedBy(c.Labels),
			Status:    c.Status,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		oi, iok := stackNames[out[i].Name]
		oj, jok := stackNames[out[j].Name]
		if iok && jok {
			return oi < oj
		}
		if iok != jok {
			return iok // known components first, in dependency order
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func containerName(c docker.Container) string {
	if len(c.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// Volumes are kept unless withVolumes: `uninstall` is too easy to type to silently destroy the database.
func (s *Service) Teardown(ctx context.Context, withVolumes bool) error {
	for _, name := range []string{
		ContainerGateway, ContainerControlPlane, ContainerRedis, ContainerPostgres,
	} {
		s.log("removing %s", name)
		if err := s.dc.RemoveContainer(ctx, name, true); err != nil && !isNotFound(err) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
		// A rollout may have died mid-flight and left its test container behind.
		_ = s.dc.RemoveContainer(ctx, name+"-test", true)
	}

	if !withVolumes {
		s.log("volumes kept (re-run `miabi setup` to restore the stack)")
		return nil
	}
	for _, v := range []string{
		VolumeGatewayProviders, VolumeGatewayCerts, VolumeGatewayConfig,
		VolumeLogs, VolumeRedisData, VolumePGData,
	} {
		s.log("removing volume %s", v)
		if err := s.dc.RemoveVolume(ctx, v, true); err != nil && !isNotFound(err) {
			return fmt.Errorf("remove volume %s: %w", v, err)
		}
	}
	// The network is left alone: user workloads are attached to it and Docker refuses to remove it in use anyway.
	return nil
}

func isNotFound(err error) bool {
	return err != nil && (strings.Contains(strings.ToLower(err.Error()), "no such") ||
		strings.Contains(strings.ToLower(err.Error()), "not found"))
}
