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
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/pkg/stack/docker"
)

// IPv6Upgrades lists the manifest's networks that want IPv6 but exist without it. Docker cannot turn
// IPv6 on for an existing network, so each has to be recreated; see AllowNetworkRecreate.
func (s *Service) IPv6Upgrades(ctx context.Context, m *Manifest) ([]string, error) {
	var out []string
	for _, n := range []NetworkConfig{m.Network, m.InternalNetwork} {
		if n.IPv6 == nil || !*n.IPv6 {
			continue
		}
		st, err := s.dc.InspectNetwork(ctx, n.Name)
		if errors.Is(err, docker.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect network %q: %w", n.Name, err)
		}
		if !st.EnableIPv6 {
			out = append(out, n.Name)
		}
	}
	return out, nil
}

// AllowNetworkRecreate lets the next converge recreate the networks IPv6Upgrades reports. It is off by
// default because a recreate briefly detaches every container on the network: only setup turns it on,
// after the operator has been shown the plan.
func (s *Service) AllowNetworkRecreate() { s.recreateNetworks = true }

type attachment struct {
	id      string
	aliases []string
}

// recreateNetwork replaces an existing network with spec, carrying every attached container across with
// its aliases, which is how gateway upstreams address apps. If the new network cannot be created, the old
// one is rebuilt from old so nothing is left detached.
func (s *Service) recreateNetwork(ctx context.Context, spec, old docker.NetworkSpec) error {
	attached, err := s.attachedTo(ctx, spec.Name)
	if err != nil {
		return err
	}
	s.log("recreating network %s with IPv6 (%d containers briefly detached)", spec.Name, len(attached))
	for _, a := range attached {
		if err := s.dc.NetworkDisconnect(ctx, spec.Name, a.id, true); err != nil {
			return fmt.Errorf("detach %s from %s: %w", shortID(a.id), spec.Name, err)
		}
	}
	if err := s.dc.RemoveNetwork(ctx, spec.Name); err != nil {
		return errors.Join(fmt.Errorf("remove network %s: %w", spec.Name, err), s.reattach(ctx, spec.Name, attached))
	}
	if _, err := s.dc.EnsureNetworkSpec(ctx, spec); err != nil {
		cerr := fmt.Errorf("create network %s with IPv6: %w", spec.Name, err)
		if _, rerr := s.dc.EnsureNetworkSpec(ctx, old); rerr != nil {
			return errors.Join(cerr, fmt.Errorf("restore network %s: %w", spec.Name, rerr),
				fmt.Errorf("containers left detached: %s", ids(attached)))
		}
		return errors.Join(cerr, s.reattach(ctx, spec.Name, attached))
	}
	return s.reattach(ctx, spec.Name, attached)
}

func (s *Service) attachedTo(ctx context.Context, network string) ([]attachment, error) {
	containers, err := s.dc.ListContainers(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	var out []attachment
	for _, c := range containers {
		full, err := s.dc.InspectContainer(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, n := range full.Networks {
			if n.Name == network {
				out = append(out, attachment{id: c.ID, aliases: n.Aliases})
				break
			}
		}
	}
	return out, nil
}

func (s *Service) reattach(ctx context.Context, network string, attached []attachment) error {
	var failed []attachment
	for _, a := range attached {
		if err := s.dc.NetworkConnect(ctx, network, a.id, a.aliases); err != nil {
			failed = append(failed, a)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not reattach to %s: %s (reconnect them, or redeploy the apps)", network, ids(failed))
	}
	return nil
}

func ids(as []attachment) string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = shortID(a.id)
	}
	return strings.Join(out, ", ")
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
