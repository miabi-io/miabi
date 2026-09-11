// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// On a swarm worker an overlay is invisible until a container attaches, so ensuring it
// would create a same-named local bridge and strand the database off the workspace network.
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
