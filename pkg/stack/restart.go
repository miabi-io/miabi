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
	"time"

	"github.com/miabi-io/miabi/pkg/stack/docker"
	"github.com/miabi-io/miabi/pkg/stack/saferollout"
)

// Generous for Postgres, which flushes on the way out.
const restartTimeout = 30

// Restarts rather than recreates, so spec changes need `miabi setup`; stores first, gateway last.
func (s *Service) Restart(ctx context.Context, m *Manifest, only string) error {
	if err := m.Normalize(); err != nil {
		return err
	}

	targets := s.components(m)
	if only != "" {
		c, ok := s.Component(m, only)
		if !ok {
			return fmt.Errorf("unknown component %q (have: %s)", only, strings.Join(s.ComponentNames(m), ", "))
		}
		targets = []component{c}
	}

	// A broken goma.yml would take down the gateway and the panel, so validate before stopping; never rewrite.
	for _, c := range targets {
		if c.Name == ContainerGateway {
			if err := s.ValidateGatewayConfig(ctx, m); err != nil {
				return err
			}
		}
	}

	for _, c := range targets {
		if err := s.restartOne(ctx, m, c); err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	return nil
}

func (s *Service) restartOne(ctx context.Context, m *Manifest, c component) error {
	cur, err := s.dc.InspectContainer(ctx, c.Name)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return fmt.Errorf("it is not running — `miabi setup` creates it")
		}
		return err
	}

	// Never restart a Compose-owned container: Compose owns its lifecycle.
	if owner := docker.ManagedBy(cur.Labels); owner != "" && owner != docker.ManagedByMiabi {
		return fmt.Errorf("it is managed by %s, not by Miabi — restart it with that "+
			"(for Compose: docker compose restart %s)", owner, c.Name)
	}

	// A restart cannot apply a spec change, so warn rather than leave the operator wondering.
	spec := c.Build(m, c.Name, *c.Image(m))
	if want, got := specHash(spec), cur.Labels[docker.LabelSpecHash]; got != "" && got != want {
		s.log("  %-14s note: the manifest has changed since this container was created — "+
			"`miabi system apply` applies that; a restart cannot", c.Name)
	}

	s.log("  %-14s restarting", c.Name)
	if err := s.dc.RestartContainer(ctx, c.Name, restartTimeout); err != nil {
		return err
	}

	// The control plane must not be restarted into a database that is still starting.
	if err := saferollout.WaitHealthy(ctx, s.dc, c.Name, 2*time.Minute); err != nil {
		return err
	}
	s.log("  %-14s healthy", c.Name)
	return nil
}

// Unlike EnsureGatewayConfig it never rewrites the file the operator just changed.
func (s *Service) ValidateGatewayConfig(ctx context.Context, m *Manifest) error {
	host, err := s.requireHostPath(ctx, s.configPath(m))
	if err != nil {
		return err
	}
	m.gatewayHostConfig = host
	return s.validateGatewayConfig(ctx, m, host)
}

func (s *Service) Pending(ctx context.Context, m *Manifest) ([]string, error) {
	if err := m.Normalize(); err != nil {
		return nil, err
	}
	var out []string
	for _, c := range s.components(m) {
		cur, err := s.dc.InspectContainer(ctx, c.Name)
		if errors.Is(err, docker.ErrNotFound) {
			out = append(out, c.Name)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", c.Name, err)
		}
		spec := c.Build(m, c.Name, *c.Image(m))
		if cur.Labels[docker.LabelSpecHash] != specHash(spec) || cur.State != "running" {
			out = append(out, c.Name)
		}
	}
	return out, nil
}
