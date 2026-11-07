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
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// Declarative sources are excluded so they stay deterministic.
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

func TestHasNodeLocalStorageHostBind(t *testing.T) {
	s := &Service{}
	if !s.hasNodeLocalStorage(&models.Application{Mounts: []models.AppMount{{HostPreset: "docker-sock", Path: "/x"}}}) {
		t.Fatal("a host-preset bind must count as node-local storage")
	}
	if s.hasNodeLocalStorage(&models.Application{}) {
		t.Fatal("an app with no mounts must not count as node-local")
	}
}

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

func (f fakeVolumes) FindByDockerName(name string) (*models.Volume, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, v := range f.byID {
		if v.DockerName == name {
			return v, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
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

// A missing row stays ErrVolumeNotFound (404); an unreadable one is ErrVolumeUnverifiable.
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

// ErrMountPathRequired means the placement checks passed, since the path check follows them.
func TestAttachVolumeNodeRules(t *testing.T) {
	vols := fakeVolumes{byID: map[uint]*models.Volume{
		1: {ID: 1, ClusterID: 2, ServerID: 10, AccessMode: models.AccessRWO},
		2: {ID: 2, ClusterID: 2, ServerID: 11, AccessMode: models.AccessRWO},
		3: {ID: 3, ClusterID: 2, ServerID: 11, AccessMode: models.AccessRWX, Driver: models.VolumeDriverNFS},
		4: {ID: 4, ClusterID: 2, ServerID: 11, AccessMode: models.AccessRWX, Driver: models.VolumeDriverHost},
		5: {ID: 5, ClusterID: 3, ServerID: 30, AccessMode: models.AccessRWX, Driver: models.VolumeDriverNFS},
	}}
	s := &Service{volumes: vols}
	container := &models.Application{WorkspaceID: 1, ClusterID: 2, ServerID: 10}
	service := &models.Application{WorkspaceID: 1, ClusterID: 2, ServerID: 10, RuntimeKind: models.RuntimeService, Replicas: 1}
	pinned := &models.Application{WorkspaceID: 1, ClusterID: 2, ServerID: 10, RuntimeKind: models.RuntimeService, Replicas: 1,
		Mounts: []models.AppMount{{VolumeID: 1, Path: "/a"}}}

	cases := []struct {
		name   string
		app    *models.Application
		volume uint
		want   error
	}{
		{"container, local volume on its node", container, 1, ErrMountPathRequired},
		{"container, local volume on another node", container, 2, ErrNodeMismatch},
		{"container, shared volume on another node", container, 3, ErrMountPathRequired},
		{"container, host-path volume", container, 4, ErrMountPathRequired},
		{"another location", container, 5, ErrVolumeLocation},
		{"service, local volume on a worker", service, 2, ErrMountPathRequired},
		{"service, second local volume on another node", pinned, 2, ErrNodeMismatch},
		{"service, shared volume beside a local one", pinned, 3, ErrMountPathRequired},
	}
	for _, tc := range cases {
		if err := s.AttachVolume(tc.app, tc.volume, ""); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}
