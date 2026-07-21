// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformstack

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// gatewayPorts are the host ports the gateway must publish: HTTP for the ACME
// challenge and the redirect, HTTPS for everything else.
var gatewayPorts = []int{80, 443}

// PortConflict is something already holding a port the gateway needs.
type PortConflict struct {
	Port int
	// Holder names what has it — a container, or the host itself when the port is
	// taken by a process Docker knows nothing about (a system nginx, Apache, or a
	// Caddy installed from a package).
	Holder string
	// Container is true when Holder is a container name, which the operator can
	// simply stop.
	Container bool
}

func (c PortConflict) String() string {
	if c.Container {
		return fmt.Sprintf("port %d is published by the container %q", c.Port, c.Holder)
	}
	return fmt.Sprintf("port %d is in use by a process on this host", c.Port)
}

// CheckPorts reports what would stop the gateway from binding 80 and 443.
//
// It runs BEFORE anything is created. Without it the install gets all the way to the
// last component — Postgres up, Redis up, the control plane running — and only then
// dies because something else already owns :443. The operator is left with a
// half-built stack and an error about a port, which is the worst possible moment to
// learn it.
//
// Two passes, because neither alone is enough:
//
//   - Docker's own containers, which is the common case (an existing gateway, a
//     Traefik, a stray nginx container) and lets the message name the culprit.
//   - An actual bind, for anything Docker cannot see. A system nginx holding :80 is
//     invisible to `docker ps`, and from inside the installer container we cannot
//     read the host's listening sockets either — our network namespace is not the
//     host's. But asking Docker to publish the port answers the question exactly:
//     it either succeeds or it does not.
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
			// Our own gateway holding the port is not a conflict — converge replaces it.
			// Neither is its test sibling from a rollout that died mid-flight.
			if name == ContainerGateway || name == ContainerGateway+"-test" {
				continue
			}
			out = append(out, PortConflict{Port: port, Holder: name, Container: true})
			continue
		}
		free, err := s.portBindable(ctx, port)
		if err != nil {
			// The probe itself failed (no helper image, no network). Do not invent a
			// conflict out of it: a false alarm here blocks a perfectly good install.
			s.log("could not probe port %d (%v) — continuing", port, err)
			continue
		}
		if !free {
			out = append(out, PortConflict{Port: port, Holder: "this host"})
		}
	}
	return out, nil
}

// ErrOrphanedData means Postgres data is already on this host, but the manifest that
// holds its password is gone.
var ErrOrphanedData = errors.New("an existing Miabi database was found, but its manifest is missing")

// CheckOrphanedData refuses a FRESH install onto an existing Postgres volume.
//
// Postgres only honours POSTGRES_PASSWORD when it initializes an EMPTY data
// directory. Point it at a data dir that already exists and it keeps the password it
// was first created with — so a new manifest, with its freshly generated password,
// can never authenticate. The database is intact and entirely unreadable.
//
// Nothing catches this on its own: `pg_isready` does not check credentials, so the
// health gate goes green, and only the control plane discovers the truth, by
// crash-looping on "password authentication failed".
//
// This is precisely why the manifest is worth backing up, and the error says so.
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

    • Restore the manifest (stack.yaml) you backed up, put it back, and re-run.
      That is the only way to keep the data.

    • Or delete the database and start over — THIS DESTROYS IT:
        docker volume rm %s`, ErrOrphanedData, VolumePGData, VolumePGData)
}

// portBindable asks Docker to publish the port on a container that exits at once. If
// something else owns it, Docker refuses at start ("port is already allocated") and
// we have our answer without ever touching the host's network namespace.
func (s *Service) portBindable(ctx context.Context, port int) (bool, error) {
	if err := ensureImage(ctx, s.dc, helperImage, s.log); err != nil {
		return false, err
	}
	spec := docker.RunSpec{
		Name:  fmt.Sprintf("mb-portcheck-%d", port),
		Image: helperImage,
		Cmd:   []string{"true"},
		Ports: map[string]string{fmt.Sprintf("%d/tcp", port): fmt.Sprintf("%d", port)},
		// Not PlatformLabels: this is a transient probe, and marking it protected would
		// make a leftover from a killed run undeletable from the containers page.
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

// isPortTaken distinguishes "the port is busy" from "the probe broke". Docker's
// wording varies by platform and version, so match on the shapes it actually uses
// rather than one exact string.
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

	// One `docker stop` per container, not per port: a proxy holding BOTH 80 and 443
	// is one container and one command, and printing it twice reads like two problems.
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
