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

package storage

import (
	"context"
	"time"

	"github.com/jkaninda/logger"
)

// WorkspaceStorage is a workspace's declared-vs-measured storage summary.
type WorkspaceStorage struct {
	DeclaredBytes int64      `json:"declared_bytes"` // SUM(SizeBytes) — asked for
	UsedBytes     int64      `json:"used_bytes"`     // SUM(UsedBytes) — measured on disk
	LimitMB       int        `json:"limit_mb"`       // effective plan MaxStorageMB (-1 = unlimited)
	MeasuredAt    *time.Time `json:"measured_at,omitempty"`
	VolumeCount   int64      `json:"volume_count"`
}

// Computed in Go, not SUM/MIN SQL, to behave the same on sqlite.
func (s *Service) WorkspaceStorage(workspaceID uint) (*WorkspaceStorage, error) {
	vols, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	out := &WorkspaceStorage{
		VolumeCount: int64(len(vols)),
		LimitMB:     -1, // unlimited unless a plan says otherwise
	}
	for i := range vols {
		out.DeclaredBytes += vols[i].SizeBytes
		out.UsedBytes += vols[i].UsedBytes
		if at := vols[i].UsedMeasuredAt; at != nil {
			if out.MeasuredAt == nil || at.Before(*out.MeasuredAt) {
				m := *at
				out.MeasuredAt = &m
			}
		}
	}
	if s.quota != nil {
		out.LimitMB = s.quota.EffectiveLimits(workspaceID).MaxStorageMB
	}
	return out, nil
}

// One docker system df per node; an unreachable node keeps its prior measurement and cron always gets nil.
func (s *Service) MeasureUsage(ctx context.Context) error {
	serverIDs, err := s.repo.ServerIDsWithVolumes()
	if err != nil {
		logger.Warn("storage usage sweep: list nodes failed", "error", err)
		return nil
	}
	var measured, nodesOK int
	for _, sid := range serverIDs {
		dc, err := s.clients.For(sid)
		if err != nil {
			logger.Warn("storage usage sweep: no client for node", "server_id", sid, "error", err)
			continue
		}
		usage, err := dc.VolumeUsage(ctx)
		if err != nil {
			logger.Warn("storage usage sweep: measure failed", "server_id", sid, "error", err)
			continue
		}
		nodesOK++
		now := time.Now()
		for _, u := range usage {
			if err := s.repo.SetUsage(u.DockerName, u.Bytes, now); err != nil {
				logger.Warn("storage usage sweep: record failed", "docker_name", u.DockerName, "error", err)
				continue
			}
			measured++
		}
	}
	measured += s.measureClassUsage(ctx)
	logger.Debug("storage usage sweep complete", "nodes", nodesOK, "volumes", measured)
	return nil
}

// docker system df sizes a bind-backed volume's stub, not its data, so class volumes are measured with du.
func (s *Service) measureClassUsage(ctx context.Context) int {
	if s.classes == nil {
		return 0
	}
	classes, err := s.classes.ListManaged()
	if err != nil {
		logger.Warn("storage usage sweep: list storage classes failed", "error", err)
		return 0
	}
	var measured int
	now := time.Now()
	for i := range classes {
		usage, err := s.classes.DiskUsageAll(ctx, &classes[i])
		if err != nil {
			logger.Warn("storage usage sweep: class measure failed", "class", classes[i].Name, "error", err)
			continue
		}
		for dockerName, bytes := range usage {
			if err := s.repo.SetUsage(dockerName, bytes, now); err != nil {
				logger.Warn("storage usage sweep: record failed", "docker_name", dockerName, "error", err)
				continue
			}
			measured++
		}
	}
	return measured
}
