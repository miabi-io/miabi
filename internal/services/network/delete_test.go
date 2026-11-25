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
)

type nodeEngine struct {
	docker.Client
	nets      []docker.Network
	removeErr error
	removed   []string
}

func (e *nodeEngine) ListNetworks(context.Context) ([]docker.Network, error) { return e.nets, nil }
func (e *nodeEngine) RemoveNetwork(_ context.Context, name string) error {
	if e.removeErr != nil {
		return e.removeErr
	}
	e.removed = append(e.removed, name)
	return nil
}

type nodeClients map[uint]docker.Client

func (c nodeClients) For(id uint) (docker.Client, error) {
	if e, ok := c[id]; ok {
		return e, nil
	}
	return nil, errors.New("node is offline")
}

type nodeList []models.Server

func (l nodeList) List() ([]models.Server, error) { return l, nil }

// Deploys copy a workspace network to every node running its apps; deleting it must not leave those copies behind.
func TestDeleteRemovesTheNetworkFromEveryNode(t *testing.T) {
	withCopy := &nodeEngine{nets: []docker.Network{{Name: "mb-ws1-abc", Scope: "local"}}}
	without := &nodeEngine{nets: []docker.Network{{Name: "mb-ws1-other", Scope: "local"}}}
	worker := &nodeEngine{nets: []docker.Network{{Name: "mb-ws1-abc", Scope: "swarm"}}, removeErr: errors.New("not a manager")}
	local := &nodeEngine{nets: []docker.Network{{Name: "mb-ws1-abc", Scope: "local"}}}
	s := &Service{
		docker:  local,
		clients: nodeClients{2: withCopy, 3: without, 4: worker},
		servers: nodeList{{ID: 1, Name: "manager", IsLocal: true}, {ID: 2, Name: "edge"}, {ID: 3, Name: "idle"}, {ID: 4, Name: "worker"}, {ID: 5, Name: "offline"}},
	}
	s.removeFromNodes(context.Background(), "mb-ws1-abc")

	if !slices.Equal(withCopy.removed, []string{"mb-ws1-abc"}) {
		t.Errorf("node with a copy removed %v, want the network", withCopy.removed)
	}
	if len(without.removed) != 0 || len(local.removed) != 0 {
		t.Errorf("removed on a node without a copy (%v) or on the control plane twice (%v)", without.removed, local.removed)
	}
}
