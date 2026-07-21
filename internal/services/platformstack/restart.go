// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformstack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/services/saferollout"
)

// restartTimeout is how long a component gets to shut down cleanly before Docker
// kills it. Generous for Postgres, which flushes on the way out.
const restartTimeout = 30

// Restart restarts the stack, or one component of it.
//
// It restarts CONTAINERS; it does not recreate them. That distinction is the whole
// point: a restart re-reads what is on disk (the gateway's bind-mounted goma.yml,
// most obviously — Goma watches its providers directory but NOT its base config, so
// an edit there does nothing until Goma is restarted). Anything that changes a
// container's SPEC — an image, an env var, a mount — needs `miabi install`, which
// recreates. Restart deliberately cannot apply those, and says so when it notices
// they are pending.
//
// Order matters for a whole-stack restart: Postgres and Redis first, and healthy
// before the control plane follows, or Miabi comes back to a database that is not
// there yet and exits. The gateway goes last, so the panel is already serving by the
// time traffic can reach it.
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

	// Restarting the gateway makes it re-read goma.yml — so a broken edit would take
	// the gateway down, and with it the panel you would use to fix it. Check the file
	// BEFORE stopping anything. (Validate only: a restart must not rewrite the config
	// out from under the operator.)
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
			return fmt.Errorf("it is not running — `miabi install` creates it")
		}
		return err
	}

	// Never restart something we do not own. On a Compose stack the containers look the
	// same and answer to the same names, but Compose owns their lifecycle — and a
	// restart that Compose did not ask for is exactly the kind of out-of-band change
	// this whole design exists to avoid.
	if owner := docker.ManagedBy(cur.Labels); owner != "" && owner != docker.ManagedByMiabi {
		return fmt.Errorf("it is managed by %s, not by Miabi — restart it with that "+
			"(for Compose: docker compose restart %s)", owner, c.Name)
	}

	// A restart cannot apply a spec change; only a recreate can. Saying so beats
	// leaving an operator to wonder why their edited manifest had no effect.
	spec := c.Build(m, c.Name, *c.Image(m))
	if want, got := specHash(spec), cur.Labels[docker.LabelSpecHash]; got != "" && got != want {
		s.log("  %-14s note: the manifest has changed since this container was created — "+
			"`miabi install` applies that; a restart cannot", c.Name)
	}

	s.log("  %-14s restarting", c.Name)
	if err := s.dc.RestartContainer(ctx, c.Name, restartTimeout); err != nil {
		return err
	}

	// Wait for it to come back before touching the next one: the control plane must not
	// be restarted into a database that is still starting.
	if err := saferollout.WaitHealthy(ctx, s.dc, c.Name, 2*time.Minute); err != nil {
		return err
	}
	s.log("  %-14s healthy", c.Name)
	return nil
}

// ValidateGatewayConfig checks the gateway config without writing it.
//
// EnsureGatewayConfig would also (re)write the file; a restart must not — the whole
// reason to restart is that the operator changed something on disk, and rewriting it
// underneath them would be the opposite of what they asked for.
func (s *Service) ValidateGatewayConfig(ctx context.Context, m *Manifest) error {
	host, err := s.requireHostPath(ctx, s.configPath(m))
	if err != nil {
		return err
	}
	m.gatewayHostConfig = host
	return s.validateGatewayConfig(ctx, m, host)
}
