// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// NodeGateways is the slice of the node service an ensurer needs: the node itself, its recoverable gateway
// token and per-node Redis password, and somewhere to record a successful deploy. Satisfied by *node.Service.
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

// Ensurer puts a node's own gateway back, the same way an agent reconnect does: mint the node's recoverable
// token, then run the idempotent Ensure. It exists so the control manager can act on a missing gateway without
// knowing how one is minted or deployed.
type Ensurer struct {
	gw      *Service
	nodes   NodeGateways
	clients Clients
}

// Ensurer builds one for this service.
func (s *Service) Ensurer(nodes NodeGateways, clients Clients) *Ensurer {
	return &Ensurer{gw: s, nodes: nodes, clients: clients}
}

// EnsureGateway redeploys the node's gateway. It refuses a node whose gateway Miabi did not deploy: an
// imported one — the platform's own on the manager, or an operator's — belongs to whoever installed it.
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
