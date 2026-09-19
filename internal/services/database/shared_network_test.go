// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// fakeNetworks stands in for the workspace network provider.
type fakeNetworks struct {
	net *models.Network
	err error
}

func (f fakeNetworks) EnsureDefault(context.Context, uint) (*models.Network, error) {
	return f.net, f.err
}

func (f fakeNetworks) Get(uint, uint) (*models.Network, error) { return f.net, f.err }

// Provisioning used to fall back to the shared proxy network when a workspace's own network could
// not be resolved — putting the database where every tenant's routed containers can reach it, and
// pinning that choice for the instance's lifetime. It must refuse instead.
func TestDefaultNetworkRefusesRatherThanShare(t *testing.T) {
	cases := []struct {
		name string
		svc  *Service
	}{
		{"provider unwired", &Service{}},
		{"provider errors", &Service{networks: fakeNetworks{err: errors.New("no pool left")}}},
		{"provider returns nothing", &Service{networks: fakeNetworks{}}},
		{"network has no docker name", &Service{networks: fakeNetworks{net: &models.Network{}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.svc.defaultNetwork(t.Context(), 1)
			if !errors.Is(err, ErrNoWorkspaceNetwork) {
				t.Fatalf("err = %v, want ErrNoWorkspaceNetwork (got network %+v)", err, got)
			}
		})
	}

	// The ordinary case still resolves.
	ok := &Service{networks: fakeNetworks{net: &models.Network{DockerName: "mb-ws7"}}}
	n, err := ok.defaultNetwork(t.Context(), 7)
	if err != nil || n == nil || n.DockerName != "mb-ws7" {
		t.Fatalf("resolved network = %+v, err = %v", n, err)
	}
}
