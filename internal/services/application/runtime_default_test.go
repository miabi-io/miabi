// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// TestDefaultToServiceRuntime pins the cluster-mode create default: an unspecified runtime becomes a
// service only when cluster mode is on AND the create is interactive. Any explicit choice opts out,
// and declarative sources are excluded so they stay deterministic.
func TestDefaultToServiceRuntime(t *testing.T) {
	cases := []struct {
		name        string
		explicit    models.RuntimeKind
		clusterOn   bool
		interactive bool
		want        bool
	}{
		{"unspecified + cluster on + interactive -> service", "", true, true, true},
		{"unspecified + cluster on + declarative -> stays container", "", true, false, false},
		{"unspecified + cluster off + interactive -> container", "", false, true, false},
		{"explicit container + cluster on + interactive -> opt out", models.RuntimeContainer, true, true, false},
		{"explicit service + cluster on -> already service", models.RuntimeService, true, true, false},
		{"explicit container + cluster off", models.RuntimeContainer, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultToServiceRuntime(tc.explicit, tc.clusterOn, tc.interactive); got != tc.want {
				t.Fatalf("defaultToServiceRuntime(%q, %v, %v) = %v, want %v", tc.explicit, tc.clusterOn, tc.interactive, got, tc.want)
			}
		})
	}
}

// TestHasNodeLocalStorageHostBind covers the volume-free branches (a privileged
// host bind is node-local; no mounts is not) without needing a volume repo.
func TestHasNodeLocalStorageHostBind(t *testing.T) {
	s := &Service{}
	if !s.hasNodeLocalStorage(&models.Application{Mounts: []models.AppMount{{HostPreset: "docker-sock", Path: "/x"}}}) {
		t.Fatal("a host-preset bind must count as node-local storage")
	}
	if s.hasNodeLocalStorage(&models.Application{}) {
		t.Fatal("an app with no mounts must not count as node-local")
	}
}

// TestRequireSharedStorageHostBind covers the host-bind rejection for a service
// (any replica count) and the container/no-mount no-ops — none touch the volume
// repo, so a zero-value Service is enough.
func TestRequireSharedStorageHostBind(t *testing.T) {
	s := &Service{}
	svcWithBind := &models.Application{
		RuntimeKind: models.RuntimeService,
		Mounts:      []models.AppMount{{HostPreset: "docker-sock", Path: "/x"}},
	}
	if err := s.requireSharedStorage(svcWithBind, 1); !errors.Is(err, ErrHostBindService) {
		t.Fatalf("service + host bind (1 replica) = %v, want ErrHostBindService", err)
	}
	container := &models.Application{RuntimeKind: models.RuntimeContainer, Mounts: svcWithBind.Mounts}
	if err := s.requireSharedStorage(container, 1); err != nil {
		t.Fatalf("container app with a host bind must be allowed, got %v", err)
	}
	if err := s.requireSharedStorage(&models.Application{RuntimeKind: models.RuntimeService}, 3); err != nil {
		t.Fatalf("service with no mounts must be allowed, got %v", err)
	}
}

type fakeVolumes struct {
	byID map[uint]*models.Volume
	err  error
}

func (f fakeVolumes) FindInWorkspace(_, id uint) (*models.Volume, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.byID[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound // what the real repository returns
	}
	return v, nil
}

func serviceApp(replicas int, volumeID uint) *models.Application {
	return &models.Application{
		RuntimeKind: models.RuntimeService,
		WorkspaceID: 1,
		Replicas:    replicas,
		Mounts:      []models.AppMount{{VolumeID: volumeID, Path: "/data"}},
	}
}

// TestRequireSharedStorageVolumeAccessMode pins the replication guard over managed
// volumes: rwx may be replicated, rwo may not, and one replica is always fine.
func TestRequireSharedStorageVolumeAccessMode(t *testing.T) {
	vols := fakeVolumes{byID: map[uint]*models.Volume{
		1: {ID: 1, AccessMode: models.AccessRWO},
		2: {ID: 2, AccessMode: models.AccessRWX},
	}}
	s := &Service{volumes: vols}

	if err := s.requireSharedStorage(serviceApp(3, 1), 3); !errors.Is(err, ErrLocalVolumeReplicated) {
		t.Fatalf("rwo volume + 3 replicas = %v, want ErrLocalVolumeReplicated", err)
	}
	if err := s.requireSharedStorage(serviceApp(1, 1), 1); err != nil {
		t.Fatalf("rwo volume + 1 replica must be allowed, got %v", err)
	}
	if err := s.requireSharedStorage(serviceApp(3, 2), 3); err != nil {
		t.Fatalf("rwx volume + 3 replicas must be allowed, got %v", err)
	}
}

func TestRequireSharedStorageFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		vols volumeLookup
		want error
	}{
		{"transient lookup error", fakeVolumes{err: errors.New("connection refused")}, ErrVolumeUnverifiable},
		{"no repository wired", nil, ErrVolumeUnverifiable},
		{"volume row missing", fakeVolumes{byID: map[uint]*models.Volume{}}, ErrVolumeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{volumes: tc.vols}
			if err := s.requireSharedStorage(serviceApp(3, 1), 3); !errors.Is(err, tc.want) {
				t.Fatalf("replicating an unreadable volume = %v, want %v", err, tc.want)
			}
			// One replica never consults the volume, so it stays allowed.
			if err := s.requireSharedStorage(serviceApp(1, 1), 1); err != nil {
				t.Fatalf("1 replica must not consult the volume, got %v", err)
			}
		})
	}
}

// TestAttachVolumeDistinguishesMissingFromUnreadable pins the two failures apart on the
// attach path, which previously reported both as "volume not found": a row that is gone
// stays ErrVolumeNotFound (404), one that could not be read is ErrVolumeUnverifiable.
// Both return before the app is touched, so a zero-value Service is enough.
func TestAttachVolumeDistinguishesMissingFromUnreadable(t *testing.T) {
	app := serviceApp(1, 1)
	missing := &Service{volumes: fakeVolumes{byID: map[uint]*models.Volume{}}}
	if err := missing.AttachVolume(app, 1, "/data"); !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("attaching a missing volume = %v, want ErrVolumeNotFound", err)
	}
	unreadable := &Service{volumes: fakeVolumes{err: errors.New("connection refused")}}
	if err := unreadable.AttachVolume(app, 1, "/data"); !errors.Is(err, ErrVolumeUnverifiable) {
		t.Fatalf("attaching an unreadable volume = %v, want ErrVolumeUnverifiable", err)
	}
}

func TestHasNodeLocalStorageFailsClosed(t *testing.T) {
	rwx := &Service{volumes: fakeVolumes{byID: map[uint]*models.Volume{1: {ID: 1, AccessMode: models.AccessRWX}}}}
	if rwx.hasNodeLocalStorage(serviceApp(1, 1)) {
		t.Fatal("an rwx volume must not count as node-local")
	}
	rwo := &Service{volumes: fakeVolumes{byID: map[uint]*models.Volume{1: {ID: 1, AccessMode: models.AccessRWO}}}}
	if !rwo.hasNodeLocalStorage(serviceApp(1, 1)) {
		t.Fatal("an rwo volume must count as node-local")
	}
	unreadable := &Service{volumes: fakeVolumes{err: errors.New("connection refused")}}
	if !unreadable.hasNodeLocalStorage(serviceApp(1, 1)) {
		t.Fatal("an unreadable volume must count as node-local")
	}
	if !(&Service{}).hasNodeLocalStorage(serviceApp(1, 1)) {
		t.Fatal("a missing volume repository must count as node-local")
	}
}
