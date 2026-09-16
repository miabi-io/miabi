// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// Starting an app whose volume is gone would hand it an empty one, so the start is refused rather than
// silently succeeding on no data.
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
