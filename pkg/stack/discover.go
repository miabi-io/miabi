// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

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

// stackRoles are the four components that ARE the stack. Membership (io.miabi.part-of=miabi) is not the test:
// the built-in registry, node agents and remote edge gateways are part of Miabi without being part of the
// stack — infrastructure the CLI neither installs nor updates. Role is the question: what IS this.
var stackRoles = map[string]bool{
	docker.RoleControlPlane:  true,
	docker.RolePlatformDB:    true,
	docker.RolePlatformCache: true,
	docker.RoleGateway:       true,
}

// stackNames is the fallback for a stack that predates platform labels — without it `miabi stack status` would report
// nothing on exactly the installs most likely to need looking at. It only helps a CLI-shaped install, since
// Compose generates <project>-<service>-<n>; a labeled Compose stack is found by role instead.
var stackNames = map[string]int{
	ContainerControlPlane: 0,
	ContainerPostgres:     1,
	ContainerRedis:        2,
	ContainerGateway:      3,
}

// Discover lists the platform stack as it actually is — by role, so it answers for a
// Compose install just as well as a CLI one. That is the point: `miabi stack status` should
// describe whatever is running, not only what the CLI put there.
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

		// Health comes from an inspect, not from the list: Docker's list API has no health field — it folds health
		// into the status string — so reading it from the list yields "" for every container and `miabi stack status` would
		// show a dash next to a healthy database. The stack is at most four containers, so inspecting each is free.
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

// Teardown removes the stack's containers. Volumes are kept unless withVolumes: the database lives in
// one, and `uninstall` is far too easy to type for it to be the thing that silently destroys it.
// Reverse dependency order — gateway, control plane, then the stores it depends on.
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
	// The network is left alone on purpose: user apps and databases are attached to
	// it, and removing it would disconnect workloads this command never claimed to
	// touch. Docker refuses to remove a network in use anyway; better to not ask.
	return nil
}

func isNotFound(err error) bool {
	return err != nil && (strings.Contains(strings.ToLower(err.Error()), "no such") ||
		strings.Contains(strings.ToLower(err.Error()), "not found"))
}
