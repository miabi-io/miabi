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

var gatewayPorts = []int{80, 443}

// PortConflict is something already holding a port the gateway needs.
type PortConflict struct {
	Port int
	// The host itself holds it when Docker can't see the process (system nginx, Apache, packaged Caddy).
	Holder    string
	Container bool
}

func (c PortConflict) String() string {
	if c.Container {
		return fmt.Sprintf("port %d is published by the container %q", c.Port, c.Holder)
	}
	return fmt.Sprintf("port %d is in use by a process on this host", c.Port)
}

// Two passes: Docker's containers to name the culprit, then a real bind for processes Docker can't see.
func (s *Service) CheckPorts(ctx context.Context) ([]PortConflict, error) {
	containers, err := s.dc.ListContainers(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	// port -> container publishing it.
	held := map[int]string{}
	for _, c := range containers {
		name := containerName(c)
		for _, p := range c.Ports {
			if p.PublicPort != 0 {
				held[int(p.PublicPort)] = name
			}
		}
	}

	var out []PortConflict
	for _, port := range gatewayPorts {
		if name, taken := held[port]; taken {
			// Our own gateway, or a test sibling from a dead rollout, is replaced by converge.
			if name == ContainerGateway || name == ContainerGateway+"-test" {
				continue
			}
			out = append(out, PortConflict{Port: port, Holder: name, Container: true})
			continue
		}
		free, err := s.portBindable(ctx, port)
		if err != nil {
			// A broken probe is not a conflict; a false alarm would block a good install.
			s.log("could not probe port %d (%v) — continuing", port, err)
			continue
		}
		if !free {
			out = append(out, PortConflict{Port: port, Holder: "this host"})
		}
	}
	return out, nil
}

var ErrOrphanedData = errors.New("an existing Miabi database was found, but its manifest is missing")

// Postgres honours POSTGRES_PASSWORD only on an empty data dir, and pg_isready doesn't check credentials.
func (s *Service) CheckOrphanedData(ctx context.Context) error {
	if _, err := s.dc.InspectVolume(ctx, VolumePGData); err != nil {
		if isNotFound(err) {
			return nil // a genuinely fresh host
		}
		return fmt.Errorf("inspect %s: %w", VolumePGData, err)
	}
	return fmt.Errorf(`%w.

  The volume %q holds a Postgres database from an earlier install. Postgres keeps the
  password its data directory was created with, so the new password this install just
  generated cannot open it — the data would be intact and unreadable.

  Either:

    • Restore the manifest (miabi.yaml) you backed up, put it back, and re-run.
      That is the only way to keep the data.

    • Or delete the database and start over — THIS DESTROYS IT:
        docker volume rm %s`, ErrOrphanedData, VolumePGData, VolumePGData)
}

// ErrRootlessDaemon is returned by CheckDaemon for a rootless Docker daemon.
var ErrRootlessDaemon = errors.New("this Docker daemon runs rootless, which Miabi does not support")

// The control plane hands containers /var/run/docker.sock, which a rootless daemon doesn't serve.
func (s *Service) CheckDaemon(ctx context.Context) error {
	rootless, err := s.dc.Rootless(ctx)
	if err != nil {
		return fmt.Errorf("read the Docker daemon's info: %w", err)
	}
	if rootless {
		return fmt.Errorf(`%w.

  Use a regular Docker daemon. To install without sudo, add your user to the
  docker group and run the installer with --non-root`, ErrRootlessDaemon)
	}
	return nil
}

// Docker refuses to start a container on a taken port, so we never touch the host's network namespace.
func (s *Service) portBindable(ctx context.Context, port int) (bool, error) {
	if err := ensureImage(ctx, s.dc, helperImage, s.log); err != nil {
		return false, err
	}
	spec := docker.RunSpec{
		Name:  fmt.Sprintf("mb-portcheck-%d", port),
		Image: helperImage,
		Cmd:   []string{"true"},
		Ports: map[string]string{fmt.Sprintf("%d/tcp", port): fmt.Sprintf("%d", port)},
		// Not PlatformLabels: a leftover transient probe must stay deletable from the containers page.
		Labels: map[string]string{docker.LabelPartOf: docker.PartOfMiabi, docker.LabelRole: "portcheck"},
	}
	_ = s.dc.RemoveContainer(ctx, spec.Name, true) // a leftover from a killed run

	_, _, err := s.dc.RunOneShot(ctx, spec)
	if err == nil {
		return true, nil
	}
	if isPortTaken(err) {
		return false, nil
	}
	return false, err
}

// Docker's wording varies by platform and version, so match the shapes it uses rather than one string.
func isPortTaken(err error) bool {
	e := strings.ToLower(err.Error())
	return strings.Contains(e, "port is already allocated") ||
		strings.Contains(e, "address already in use") ||
		strings.Contains(e, "bind: permission denied") || // a rootless daemon cannot take :80 at all
		strings.Contains(e, "ports are not available")
}

// PortConflictError renders conflicts as something an operator can act on.
func PortConflictError(conflicts []PortConflict) error {
	var b strings.Builder
	b.WriteString("the gateway needs ports 80 and 443, and they are not free:\n")
	for _, c := range conflicts {
		b.WriteString("    • " + c.String() + "\n")
	}
	b.WriteString("\n  Stop whatever is holding them, then re-run.")

	// One `docker stop` per container, not per port, so one proxy isn't reported as two problems.
	seen := map[string]bool{}
	for _, c := range conflicts {
		if c.Container && !seen[c.Holder] {
			seen[c.Holder] = true
			b.WriteString(fmt.Sprintf("\n    docker stop %s", c.Holder))
		}
	}
	b.WriteString("\n\n  If you already run Miabi under Docker Compose, that is what holds them —\n" +
		"  this installer would take over the same ports. Bring the Compose stack down first:\n" +
		"    docker compose down     (your data volumes survive this)")
	return fmt.Errorf("%s", b.String())
}
