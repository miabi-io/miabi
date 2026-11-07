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

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// Adopter records an existing container as a node's gateway. Satisfied by *node.Service.
type Adopter interface {
	AdoptGateway(id uint, container, image, configYAML string) (*models.Server, error)
}

// Marking it imported stops Miabi deploying mb-node-gateway beside it; its config is not copied since the stack owns it.
func AdoptCentral(ctx context.Context, dc docker.Client, adopter Adopter, local *models.Server) {
	if local == nil || adopter == nil || dc == nil {
		return
	}
	if tracked(ctx, dc, local) {
		return
	}
	// A manager that runs a gateway Miabi deployed itself is already managed; leave it alone.
	if _, err := dc.InspectContainer(ctx, ContainerName); err == nil {
		return
	}
	c, found := FindCentral(ctx, dc)
	if !found {
		return // no platform gateway on this host: a manual install, or one that serves ingress elsewhere
	}
	name := ContainerNameOf(c)
	if name == "" {
		name = c.ID
	}
	if _, err := adopter.AdoptGateway(local.ID, name, c.Image, ""); err != nil {
		logger.Warn("could not adopt the platform gateway as the manager's", "container", name, "error", err)
		return
	}
	logger.Info("adopted the platform gateway as the manager's edge gateway", "container", name, "image", c.Image)
}

// A tracked container that is gone is drift to report, not a reason to adopt something else.
func tracked(ctx context.Context, dc docker.Client, srv *models.Server) bool {
	if srv.GatewayContainer == "" {
		return false
	}
	_, err := dc.InspectContainer(ctx, srv.GatewayContainer)
	return err == nil
}
