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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/datavolume"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type guardEngine struct {
	docker.Client
	volumes map[string]docker.Volume
}

func (g guardEngine) InspectVolume(_ context.Context, name string) (docker.Volume, error) {
	if v, ok := g.volumes[name]; ok {
		return v, nil
	}
	return docker.Volume{}, docker.ErrNotFound
}

type guardClients map[uint]docker.Client

func (g guardClients) For(serverID uint) (docker.Client, error) {
	if dc, ok := g[serverID]; ok {
		return dc, nil
	}
	return nil, errors.New("node is offline")
}
func (g guardClients) LocalID() uint { return 1 }

func guardedInstance() *models.DatabaseInstance {
	return &models.DatabaseInstance{
		ID: 5, WorkspaceID: 1, Name: "main", ServerID: 1, ContainerID: "c5",
		Status: models.DBStatusStopped, VolumeName: "mb-db-x-5-data",
		VolumeEngineCreatedAt: "2026-09-01T10:00:00Z",
	}
}

// A missing data volume would be recreated empty and initialized over; the data must be restored first.
func TestStartAndRestartRefuseAnInstanceWhoseDataIsGone(t *testing.T) {
	s := &Service{clients: guardClients{1: guardEngine{volumes: map[string]docker.Volume{}}}}
	inst := guardedInstance()

	if err := s.Start(context.Background(), inst); !errors.Is(err, datavolume.ErrLost) {
		t.Fatalf("Start err = %v; want datavolume.ErrLost", err)
	}
	if err := s.Restart(context.Background(), inst); !errors.Is(err, datavolume.ErrLost) {
		t.Fatalf("Restart err = %v; want datavolume.ErrLost", err)
	}
	// The instance is left as it was: nothing started, nothing recorded as running.
	if inst.Status != models.DBStatusStopped {
		t.Fatalf("status = %q; want it untouched", inst.Status)
	}
}

func TestGuardAllowsAnIntactOrUnrecordedVolume(t *testing.T) {
	s := &Service{clients: guardClients{1: guardEngine{volumes: map[string]docker.Volume{
		"mb-db-x-5-data": {Name: "mb-db-x-5-data", CreatedAt: "2026-09-01T10:00:00Z"},
	}}}}
	if err := s.guardData(context.Background(), guardedInstance()); err != nil {
		t.Fatalf("intact volume: %v; want allowed", err)
	}

	// An instance with no volume recorded yet (provisioning) has nothing to guard.
	fresh := guardedInstance()
	fresh.VolumeName = ""
	if err := s.guardData(context.Background(), fresh); err != nil {
		t.Fatalf("instance with no volume: %v; want allowed", err)
	}
}
