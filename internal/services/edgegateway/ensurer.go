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

package edgegateway

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type NodeGateways interface {
	Get(id uint) (*models.Server, error)
	GatewayToken(id uint) (string, error)
	GatewayRedisPassword(id uint) (string, error)
	MarkGatewayDeployed(id uint)
}

// Clients resolves a node's engine. Satisfied by *nodes.Clients.
type Clients interface {
	For(serverID uint) (docker.Client, error)
}

// Ensurer lets the control manager restore a missing gateway without knowing how one is minted or deployed.
type Ensurer struct {
	gw      *Service
	nodes   NodeGateways
	clients Clients
}

// Ensurer builds one for this service.
func (s *Service) Ensurer(nodes NodeGateways, clients Clients) *Ensurer {
	return &Ensurer{gw: s, nodes: nodes, clients: clients}
}

// EnsureGateway refuses imported gateways, which belong to whoever installed them.
func (e *Ensurer) EnsureGateway(ctx context.Context, serverID uint) error {
	srv, err := e.nodes.Get(serverID)
	if err != nil {
		return err
	}
	if !AutoDeploy(srv) {
		return fmt.Errorf("node %s runs an imported gateway, which Miabi does not deploy", srv.Name)
	}
	dc, err := e.clients.For(serverID)
	if err != nil {
		return err
	}
	token, err := e.nodes.GatewayToken(serverID)
	if err != nil {
		return err
	}
	// Remote edge nodes run their own Redis; the manager reuses the platform one.
	var redisPassword string
	if !srv.IsLocal {
		if pw, perr := e.nodes.GatewayRedisPassword(serverID); perr == nil {
			redisPassword = pw
		}
	}
	if err := e.gw.Ensure(ctx, dc, srv, token, redisPassword); err != nil {
		return err
	}
	e.nodes.MarkGatewayDeployed(serverID)
	return nil
}
