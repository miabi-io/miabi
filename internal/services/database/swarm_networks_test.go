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
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type ensureRecorder struct {
	docker.Client
	ensured []string
}

func (r *ensureRecorder) EnsureNetwork(_ context.Context, name string) (string, error) {
	r.ensured = append(r.ensured, name)
	return name, nil
}

type swarmRecorder struct{ ensured []string }

func (r *swarmRecorder) WorkspaceOverlay(clusterID uint, _ models.Network) bool {
	return clusterID == 3
}
func (r *swarmRecorder) EnsureWorkspaceOverlay(_ context.Context, _ uint, n models.Network) error {
	r.ensured = append(r.ensured, n.DockerName)
	return nil
}

func TestRemoteSwarmNetworksAreCreatedThroughTheSwarm(t *testing.T) {
	inst := &models.DatabaseInstance{
		ClusterID:   3,
		NetworkName: "mb-ws-1-default",
		Networks:    []models.Network{{DockerName: "mb-ws-1-default", Driver: "bridge"}},
	}
	dc := &ensureRecorder{}
	sw := &swarmRecorder{}
	s := &Service{}
	s.SetSwarmNetworks(sw)

	if _, err := s.ensureInstanceNetworks(context.Background(), dc, inst); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !slices.Equal(sw.ensured, []string{"mb-ws-1-default"}) || len(dc.ensured) != 0 {
		t.Errorf("swarm ensured %v, node ensured %v; want the overlay through the swarm and no bridge", sw.ensured, dc.ensured)
	}
}

// On a swarm worker an overlay is invisible until attached, so ensuring it would create a same-named local bridge.
func TestInstanceOverlaysAreNotEnsuredOnTheNode(t *testing.T) {
	inst := &models.DatabaseInstance{
		NetworkName: "mb-ws-1-default",
		Networks: []models.Network{
			{DockerName: "mb-ws-1-default", Driver: "overlay"},
			{DockerName: "mb-ws-1-extra", Driver: "bridge"},
		},
	}
	dc := &ensureRecorder{}

	names, err := (&Service{}).ensureInstanceNetworks(context.Background(), dc, inst)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !slices.Equal(names, []string{"mb-ws-1-default", "mb-ws-1-extra"}) {
		t.Errorf("names = %v, want both networks", names)
	}
	if !slices.Equal(dc.ensured, []string{"mb-ws-1-extra"}) {
		t.Errorf("ensured = %v, want only the bridge", dc.ensured)
	}
}
