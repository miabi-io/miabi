// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"context"
	"errors"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

// Runtime is the live state of the registry container, the only place its resource usage surfaces.
type Runtime struct {
	// Configured-and-enabled says nothing about whether the registry is actually serving.
	Running bool `json:"running"`
	// Docker's own words, shown as-is so a stuck container isn't flattened to "not running".
	State  string `json:"state,omitempty"`
	Status string `json:"status,omitempty"`
	Health string `json:"health,omitempty"`
	// Surfaces a crash-looping registry, which otherwise looks healthy between restarts.
	RestartCount int    `json:"restart_count,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	Image        string `json:"image,omitempty"`

	// Stats is a live sample; zero when the container is not running.
	Stats *docker.StatsSample `json:"stats,omitempty"`
	// Lets the UI say "usage unavailable" instead of rendering a confident 0%.
	StatsError string `json:"stats_error,omitempty"`
}

var errStopStream = errors.New("stop")

// Two readings are needed for a CPU percentage, and the daemon emits one per second.
const statsTimeout = 5 * time.Second

// RuntimeStatus reports a missing container as Running=false; only a Docker failure is an error.
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

// RepositoryCount counts repositories across all workspaces; a down or empty registry reports 0.
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

// A single one-shot sample always reports ~0% CPU, so two stream samples are read.
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
