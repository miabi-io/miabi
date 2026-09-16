// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// AdoptCentral records the platform's own gateway — the Goma container the Miabi stack installs on the
// manager — as the manager node's edge gateway. Run once at boot.
//
// A fresh install otherwise leaves the operator to import it by hand before the manager's gateway panel shows
// anything at all. Adoption also marks it imported, and that is what stops Miabi from ever deploying
// mb-node-gateway beside it, where the two would fight over ports 80 and 443.
//
// The container's config is deliberately not copied onto the node: the stack owns that container's spec, so an
// edit Miabi could not apply would be a lie. Nothing here ever recreates or stops it.
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

// tracked reports whether the node already has a gateway Miabi knows about that still exists. A tracked
// container that is gone is drift for the control manager to report, not a reason to adopt something else.
func tracked(ctx context.Context, dc docker.Client, srv *models.Server) bool {
	if srv.GatewayContainer == "" {
		return false
	}
	_, err := dc.InspectContainer(ctx, srv.GatewayContainer)
	return err == nil
}
