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

// Falling back to the shared proxy network would expose the DB to every tenant for its lifetime; it must refuse.
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
