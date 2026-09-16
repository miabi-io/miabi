// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package datavolume

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type stubEngine struct {
	docker.Client
	volumes map[string]docker.Volume
	err     error
}

func (s stubEngine) InspectVolume(_ context.Context, name string) (docker.Volume, error) {
	if s.err != nil {
		return docker.Volume{}, s.err
	}
	if v, ok := s.volumes[name]; ok {
		return v, nil
	}
	return docker.Volume{}, docker.ErrNotFound
}

type stubClients struct {
	engines map[uint]docker.Client
}

func (s stubClients) For(serverID uint) (docker.Client, error) {
	if e, ok := s.engines[serverID]; ok {
		return e, nil
	}
	return nil, errors.New("node is offline")
}

type stubVolumes map[uint]*models.Volume

func (s stubVolumes) FindInWorkspace(_, id uint) (*models.Volume, error) {
	if v, ok := s[id]; ok {
		return v, nil
	}
	return nil, errors.New("record not found")
}

func (s stubVolumes) FindByDockerName(name string) (*models.Volume, error) {
	for _, v := range s {
		if v.DockerName == name {
			return v, nil
		}
	}
	return nil, errors.New("record not found")
}

func appWith(mounts ...models.AppMount) *models.Application {
	return &models.Application{ID: 7, WorkspaceID: 1, Name: "api", ServerID: 1, Mounts: mounts}
}

func localVolume(id, serverID uint, name, recorded string) *models.Volume {
	return &models.Volume{
		ID: id, WorkspaceID: 1, Name: name, DockerName: "mb-vol-1-" + name, ServerID: serverID,
		Driver: models.VolumeDriverLocal, EngineCreatedAt: recorded,
	}
}

func TestCheckAppRefusesAMissingVolume(t *testing.T) {
	vols := stubVolumes{3: localVolume(3, 1, "uploads", "2026-09-01T10:00:00Z")}
	clients := stubClients{engines: map[uint]docker.Client{1: stubEngine{volumes: map[string]docker.Volume{}}}}

	err := CheckApp(context.Background(), clients, vols, appWith(models.AppMount{VolumeID: 3, Path: "/data"}))
	if !errors.Is(err, ErrLost) {
		t.Fatalf("err = %v; want ErrLost", err)
	}
	// The refusal names the volume as the user knows it.
	if got := err.Error(); !contains(got, "uploads") || !contains(got, "missing") {
		t.Fatalf("err = %q; want it to name the volume and say it is missing", got)
	}
}

func TestCheckAppRefusesAReplacedVolume(t *testing.T) {
	vols := stubVolumes{3: localVolume(3, 1, "uploads", "2026-09-01T10:00:00Z")}
	clients := stubClients{engines: map[uint]docker.Client{1: stubEngine{volumes: map[string]docker.Volume{
		"mb-vol-1-uploads": {Name: "mb-vol-1-uploads", CreatedAt: "2026-09-16T02:00:00Z"},
	}}}}

	err := CheckApp(context.Background(), clients, vols, appWith(models.AppMount{VolumeID: 3, Path: "/data"}))
	if !errors.Is(err, ErrLost) || !contains(err.Error(), "replaced") {
		t.Fatalf("err = %v; want ErrLost saying the volume was replaced", err)
	}
}

func TestCheckAppAllowsIntactAndUnrecordedVolumes(t *testing.T) {
	vols := stubVolumes{
		3: localVolume(3, 1, "uploads", "2026-09-01T10:00:00Z"),
		4: localVolume(4, 1, "cache", ""), // created before Miabi recorded a timestamp
	}
	clients := stubClients{engines: map[uint]docker.Client{1: stubEngine{volumes: map[string]docker.Volume{
		"mb-vol-1-uploads": {Name: "mb-vol-1-uploads", CreatedAt: "2026-09-01T10:00:00Z"},
		"mb-vol-1-cache":   {Name: "mb-vol-1-cache", CreatedAt: "2026-05-04T09:00:00Z"},
	}}}}

	app := appWith(models.AppMount{VolumeID: 3, Path: "/data"}, models.AppMount{VolumeID: 4, Path: "/cache"})
	if err := CheckApp(context.Background(), clients, vols, app); err != nil {
		t.Fatalf("err = %v; want the start allowed", err)
	}
}

// An engine that cannot answer is not evidence of loss: the start fails on its own if the node is down,
// and refusing here would turn every unreachable node into a data-loss report.
func TestCheckAppAllowsWhenTheEngineCannotAnswer(t *testing.T) {
	vols := stubVolumes{3: localVolume(3, 2, "uploads", "2026-09-01T10:00:00Z")}
	app := appWith(models.AppMount{VolumeID: 3, Path: "/data"})

	offline := stubClients{engines: map[uint]docker.Client{}}
	if err := CheckApp(context.Background(), offline, vols, app); err != nil {
		t.Fatalf("offline node: err = %v; want nil", err)
	}

	broken := stubClients{engines: map[uint]docker.Client{2: stubEngine{err: errors.New("engine says no")}}}
	if err := CheckApp(context.Background(), broken, vols, app); err != nil {
		t.Fatalf("unreadable engine: err = %v; want nil", err)
	}
}

// Host binds, presets and config projections are not Docker volumes, and shared storage lives on the
// backend rather than in a volume object each node creates on demand.
func TestCheckAppSkipsWhatItCannotJudge(t *testing.T) {
	shared := localVolume(5, 1, "media", "2026-09-01T10:00:00Z")
	shared.Driver, shared.AccessMode = models.VolumeDriverNFS, models.AccessRWX
	vols := stubVolumes{5: shared}
	clients := stubClients{engines: map[uint]docker.Client{1: stubEngine{volumes: map[string]docker.Volume{}}}}

	app := appWith(
		models.AppMount{HostPreset: "docker-socket", Path: "/var/run/docker.sock"},
		models.AppMount{HostPath: "/mnt/data", Path: "/data"},
		models.AppMount{ConfigID: 9, ConfigKey: "nginx.conf", Path: "/etc/nginx/nginx.conf"},
		models.AppMount{VolumeID: 5, Path: "/media"},
	)
	if err := CheckApp(context.Background(), clients, vols, app); err != nil {
		t.Fatalf("err = %v; want nil for mounts the guard cannot judge", err)
	}
}

func TestCheckVolumeRefusesAMissingDatabaseVolume(t *testing.T) {
	clients := stubClients{engines: map[uint]docker.Client{1: stubEngine{volumes: map[string]docker.Volume{}}}}

	err := CheckVolume(context.Background(), clients, 1, "mb-db-x-5-data", "2026-09-01T10:00:00Z", "main")
	if !errors.Is(err, ErrLost) || !contains(err.Error(), "main") {
		t.Fatalf("err = %v; want ErrLost naming the instance", err)
	}
	// An instance with no recorded volume has nothing to check.
	if err := CheckVolume(context.Background(), clients, 1, "", "", "main"); err != nil {
		t.Fatalf("err = %v; want nil when there is no volume", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
