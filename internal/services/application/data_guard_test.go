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

package application

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
func (g guardClients) ForServiceTask(context.Context, uint, string) (docker.Client, string, error) {
	return nil, "", docker.ErrNotFound
}

func guardService(live map[string]docker.Volume) *Service {
	return &Service{
		clients: guardClients{1: guardEngine{volumes: live}},
		volumes: fakeVolumes{byID: map[uint]*models.Volume{
			3: {ID: 3, WorkspaceID: 1, Name: "uploads", DockerName: "mb-vol-1-uploads", ServerID: 1,
				Driver: models.VolumeDriverLocal, EngineCreatedAt: "2026-09-01T10:00:00Z"},
		}},
	}
}

func guardedApp() *models.Application {
	return &models.Application{
		ID: 7, WorkspaceID: 1, Name: "api", ServerID: 1, Status: models.AppStatusStopped,
		Mounts: []models.AppMount{{VolumeID: 3, DockerName: "mb-vol-1-uploads", Path: "/data"}},
	}
}

// A missing volume would be replaced by an empty one, so start is refused.
func TestStartAndRestartRefuseAnAppWhoseDataIsGone(t *testing.T) {
	s := guardService(map[string]docker.Volume{}) // the volume is missing on its node
	app := guardedApp()

	if _, err := s.Start(context.Background(), app); !errors.Is(err, datavolume.ErrLost) {
		t.Fatalf("Start err = %v; want datavolume.ErrLost", err)
	}
	if _, err := s.Restart(context.Background(), app); !errors.Is(err, datavolume.ErrLost) {
		t.Fatalf("Restart err = %v; want datavolume.ErrLost", err)
	}
}

func TestStartAllowsAnAppWhoseVolumeIsIntact(t *testing.T) {
	s := guardService(map[string]docker.Volume{
		"mb-vol-1-uploads": {Name: "mb-vol-1-uploads", CreatedAt: "2026-09-01T10:00:00Z"},
	})

	if err := s.guardData(context.Background(), guardedApp()); err != nil {
		t.Fatalf("guardData = %v; want the start allowed", err)
	}
}
