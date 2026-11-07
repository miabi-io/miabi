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

package worker

import (
	"context"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// ProxyNetworkReconciler keeps only route-exposed apps on the shared proxy network, on local and remote nodes.
type ProxyNetworkReconciler struct {
	apps     *repositories.ApplicationRepository
	releases *repositories.ReleaseRepository
	clients  NodeDocker
	cluster  ClusterCap
}

func NewProxyNetworkReconciler(apps *repositories.ApplicationRepository, releases *repositories.ReleaseRepository, clients NodeDocker) *ProxyNetworkReconciler {
	return &ProxyNetworkReconciler{apps: apps, releases: releases, clients: clients}
}

func (r *ProxyNetworkReconciler) SetCluster(c ClusterCap) { r.cluster = c }

// Idempotent and best-effort: a missing container or offline node is a no-op, so it never blocks a route change.
func (r *ProxyNetworkReconciler) ReconcileProxyAttachment(ctx context.Context, appID uint, attached bool) error {
	app, err := r.apps.FindByID(appID)
	if err != nil {
		return err
	}
	// Service apps have no node-local container; the gateway must join the overlay to reach the VIP.
	if app.RuntimeKind == models.RuntimeService {
		return r.reconcileServiceIngress(ctx, app, attached)
	}
	eng, err := r.clients.For(app.ServerID)
	if err != nil {
		return nil // node offline: reconciles again on next deploy/route change
	}
	var targets []attachTarget
	if rel, err := r.releases.FindActive(appID); err == nil && rel.ContainerID != "" {
		targets = append(targets, attachTarget{rel.ContainerID, node.AppAlias(app)})
	}
	if app.CanaryReleaseID != nil {
		if rel, err := r.releases.FindByID(*app.CanaryReleaseID); err == nil && rel.ContainerID != "" {
			targets = append(targets, attachTarget{rel.ContainerID, node.CanaryAlias(app)})
		}
	}
	r.applyAttachment(ctx, eng, app.ClusterID, targets, attached)
	return nil
}

type attachTarget struct {
	id    string
	alias string
}

// Mirrors buildRuntimeContext so a container started before cluster mode becomes reachable without a redeploy.
func (r *ProxyNetworkReconciler) applyAttachment(ctx context.Context, eng docker.Client, clusterID uint, targets []attachTarget, attached bool) {
	networks := []string{node.AppNetwork}
	if r.cluster != nil && r.cluster.IsSwarm(clusterID) {
		networks = append(networks, node.IngressOverlay)
	}
	for _, t := range targets {
		for _, n := range networks {
			if attached {
				_ = eng.NetworkConnect(ctx, n, t.id, []string{t.alias})
			} else {
				_ = eng.NetworkDisconnect(ctx, n, t.id, true)
			}
		}
	}
}

// Swarm has no live network connect for a service, so a change is a rolling update; a no-op when already set.
func (r *ProxyNetworkReconciler) reconcileServiceIngress(ctx context.Context, app *models.Application, attached bool) error {
	if r.cluster != nil {
		if mgr, err := r.cluster.Manager(ctx, app.ClusterID); err == nil {
			alias := node.AppAlias(app)
			if err := mgr.ServiceSetIngressNetwork(ctx, alias, node.IngressOverlay, alias, attached); err != nil {
				logger.Warn("failed to reconcile service ingress attachment",
					"app", app.ID, "service", alias, "attached", attached, "error", err)
			}
		}
	}
	if !attached {
		// Every other clustered app still needs the shared gateway, and leaving it attached is harmless.
		return nil
	}
	return r.ReconcileIngressGateway(ctx)
}

// Re-asserts an attachment a compose recreate silently drops; a no-op off cluster mode.
func (r *ProxyNetworkReconciler) ReconcileIngressGateway(ctx context.Context) error {
	mgr, err := r.clients.For(0)
	if r.cluster != nil {
		mgr, err = r.cluster.Manager(ctx, models.DefaultClusterID)
	}
	if err != nil {
		return nil
	}
	if _, err := mgr.CreateOverlayNetwork(ctx, node.IngressOverlay); err != nil {
		return nil // not a swarm manager yet, or overlay unavailable
	}
	gw, err := centralGatewayContainer(ctx, mgr)
	if err != nil || gw == "" {
		return nil // gateway not found/up yet; retries on the next reconcile
	}
	_ = mgr.NetworkConnect(ctx, node.IngressOverlay, gw, nil)
	return nil
}

// The role label survives a non-default compose project name; the name fallback is for pre-label stacks.
func centralGatewayContainer(ctx context.Context, mgr docker.Client) (string, error) {
	list, err := mgr.ListContainers(ctx, false)
	if err != nil {
		return "", err
	}
	var byName string
	for i := range list {
		c := list[i]
		if c.Labels[docker.LabelRole] == edgegateway.CentralRoleValue {
			return c.ID, nil
		}
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == edgegateway.CentralContainerName {
				byName = c.ID
			}
		}
	}
	if byName != "" {
		logger.Warn("central gateway found by container name, not by label — this stack predates platform labels; "+
			"recreate it (docker compose up -d) so it is labeled, otherwise a custom compose project name will break ingress",
			"container", byName, "expected_label", docker.LabelRole+"="+edgegateway.CentralRoleValue)
	}
	return byName, nil
}
