// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// Starting an instance whose data volume is gone would create an empty one and initialize a new, empty
// database over it. Refusing is the only safe answer; the data has to be restored first.
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
