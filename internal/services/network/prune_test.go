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

package network

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type pruneDocker struct {
	docker.Client
	networks []docker.Network
	inUse    map[string]bool
	removed  []string
}

func (f *pruneDocker) ListNetworks(context.Context) ([]docker.Network, error) { return f.networks, nil }

func (f *pruneDocker) RemoveNetwork(_ context.Context, name string) error {
	if f.inUse[name] {
		return errors.New("error while removing network: network has active endpoints")
	}
	f.removed = append(f.removed, name)
	return nil
}

func managedBridge(name string) docker.Network {
	return docker.Network{Name: name, Driver: DriverBridge, Labels: map[string]string{docker.ManagedLabel: "true"}}
}

func pruneService(t *testing.T, clusterOn bool, records ...string) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Network{}); err != nil {
		t.Fatal(err)
	}
	for i, name := range records {
		db.Create(&models.Network{WorkspaceID: 1, Name: name, DockerName: name, Driver: DriverBridge, IsDefault: i == 0})
	}
	s := NewService(repositories.NewNetworkRepository(db), nil)
	s.SetCluster(fakeCluster{on: clusterOn})
	return s
}

// Bridges left by conversion collide with overlays that took their subnets; only those are removed.
func TestPruneStaleBridgesRemovesOnlyLeftovers(t *testing.T) {
	s := pruneService(t, true, "mb-ws3-kept")
	dc := &pruneDocker{networks: []docker.Network{
		managedBridge("mb-ws1-0a0e9d8c5dd1"),
		managedBridge("mb-ws2-5c006d3542ae"),
		managedBridge("mb-ws3-kept"),
		{Name: "mb-ws2-ec333153e7b0-ov", Driver: "overlay", Labels: map[string]string{docker.ManagedLabel: "true"}},
		{Name: "mb-ws9-not-ours", Driver: DriverBridge},
		managedBridge("miabi-proxy"),
	}}

	removed := s.PruneStaleBridges(context.Background(), dc, "meetup-02")

	want := []string{"mb-ws1-0a0e9d8c5dd1", "mb-ws2-5c006d3542ae"}
	if !slices.Equal(removed, want) || !slices.Equal(dc.removed, want) {
		t.Fatalf("removed %v, want %v", dc.removed, want)
	}
}

func TestPruneStaleBridgesKeepsABridgeStillInUse(t *testing.T) {
	s := pruneService(t, true)
	dc := &pruneDocker{networks: []docker.Network{managedBridge("mb-ws1-busy")}, inUse: map[string]bool{"mb-ws1-busy": true}}

	if removed := s.PruneStaleBridges(context.Background(), dc, "n"); len(removed) != 0 {
		t.Fatalf("removed %v, want the in-use bridge kept", removed)
	}
}

// Without cluster networking a workspace bridge is how a node reaches its workspace, never a leftover.
func TestPruneStaleBridgesDoesNothingOutsideClusterMode(t *testing.T) {
	s := pruneService(t, false)
	dc := &pruneDocker{networks: []docker.Network{managedBridge("mb-ws1-0a0e9d8c5dd1")}}

	if removed := s.PruneStaleBridges(context.Background(), dc, "n"); len(removed) != 0 || len(dc.removed) != 0 {
		t.Fatalf("removed %v outside cluster mode", dc.removed)
	}
}
