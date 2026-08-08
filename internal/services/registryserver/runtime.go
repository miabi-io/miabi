// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"context"
	"errors"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

// Runtime is the live state of the registry container: whether it is running, and what it is costing. The
// registry is infrastructure an admin can't see with the app-level tooling — it belongs to no workspace —
// so this is the only place its resource usage surfaces.
type Runtime struct {
	// Running is the single question the UI leads with: is the registry actually
	// serving? Configured-and-enabled says nothing about that.
	Running bool `json:"running"`
	// State / Status are Docker's own words ("running", "Up 3 days"), shown as-is
	// so a stuck container is recognisable rather than flattened to "not running".
	State  string `json:"state,omitempty"`
	Status string `json:"status,omitempty"`
	Health string `json:"health,omitempty"`
	// RestartCount surfaces a crash-looping registry, which otherwise looks
	// healthy in snapshots taken between restarts.
	RestartCount int    `json:"restart_count,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	Image        string `json:"image,omitempty"`

	// Stats is a live sample; zero when the container is not running.
	Stats *docker.StatsSample `json:"stats,omitempty"`
	// StatsError explains a sample that could not be taken, so the UI can say
	// "usage unavailable" instead of rendering a confident 0%.
	StatsError string `json:"stats_error,omitempty"`
}

var errStopStream = errors.New("stop")

// statsTimeout bounds a sample. Two readings are needed for a CPU percentage, and
// the daemon emits one per second.
const statsTimeout = 5 * time.Second

// RuntimeStatus inspects the registry container and samples its resource usage. A missing container is not
// an error: a registry that is disabled, or enabled but held down by a storage problem, simply reports
// Running=false. Only a Docker failure is surfaced as one.
func (s *Service) RuntimeStatus(ctx context.Context, dc docker.Client) (*Runtime, error) {
	if dc == nil {
		return nil, errors.New("docker is unavailable on the control plane")
	}
	c, err := dc.InspectContainer(ctx, ContainerName)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return &Runtime{}, nil // not created: disabled, or never started
		}
		return nil, err
	}
	rt := &Runtime{
		Running:      c.State == "running",
		State:        c.State,
		Status:       c.Status,
		Health:       c.Health,
		RestartCount: c.RestartCount,
		StartedAt:    c.StartedAt,
		Image:        c.Image,
	}
	if !rt.Running {
		return rt, nil
	}
	sample, sErr := s.sample(ctx, dc, c.ID)
	if sErr != nil {
		rt.StatsError = sErr.Error()
		return rt, nil
	}
	rt.Stats = &sample
	return rt, nil
}

// RepositoryCount is the number of repositories the registry holds across every workspace. It answers one
// question: would changing where blobs live strand anything? A registry that is down or empty reports 0,
// which is the same answer for that purpose.
func (s *Service) RepositoryCount(ctx context.Context) (int, error) {
	if s.reg == nil {
		return 0, nil
	}
	repos, err := s.reg.Catalog(ctx)
	if err != nil {
		return 0, err
	}
	return len(repos), nil
}

// sample reads two stream samples so CPU is computed against a prior reading — a
// single one-shot sample always reports ~0%.
func (s *Service) sample(ctx context.Context, dc docker.Client, containerID string) (docker.StatsSample, error) {
	sctx, cancel := context.WithTimeout(ctx, statsTimeout)
	defer cancel()
	var last docker.StatsSample
	n := 0
	err := dc.StreamStats(sctx, containerID, func(x docker.StatsSample) error {
		last = x
		n++
		if n >= 2 {
			return errStopStream
		}
		return nil
	})
	if n == 0 {
		return last, err
	}
	return last, nil
}
