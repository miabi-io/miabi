// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// ProxyNetworkReconciler attaches or detaches an application's running
// container(s) from the shared reverse-proxy network so only route-exposed apps
// stay on it. Runs on the Docker engine of the node the app lives on, so it
// works for local and remote (tunneled) nodes alike. Satisfies the route
// service's ProxyAttacher.
type ProxyNetworkReconciler struct {
	apps     *repositories.ApplicationRepository
	releases *repositories.ReleaseRepository
	clients  NodeDocker
}

func NewProxyNetworkReconciler(apps *repositories.ApplicationRepository, releases *repositories.ReleaseRepository, clients NodeDocker) *ProxyNetworkReconciler {
	return &ProxyNetworkReconciler{apps: apps, releases: releases, clients: clients}
}

// ReconcileProxyAttachment connects (attached) or disconnects the app's active
// and canary release containers from the proxy network, with the app's stable
// DNS alias. Idempotent and best-effort: a missing container or offline node is
// a no-op, so this never blocks a route change.
func (r *ProxyNetworkReconciler) ReconcileProxyAttachment(ctx context.Context, appID uint, attached bool) error {
	app, err := r.apps.FindByID(appID)
	if err != nil {
		return err
	}
	// Cluster (service) apps have no node-local container to attach; instead the
	// central gateway must join the workspace overlay to reach the service VIP.
	if app.RuntimeKind == models.RuntimeService {
		return r.reconcileServiceIngress(ctx, app, attached)
	}
	eng, err := r.clients.For(app.ServerID)
	if err != nil {
		return nil // node offline: reconciles again on next deploy/route change
	}
	type target struct {
		id    string
		alias string
	}
	var targets []target
	if rel, err := r.releases.FindActive(appID); err == nil && rel.ContainerID != "" {
		targets = append(targets, target{rel.ContainerID, node.AppAlias(app)})
	}
	if app.CanaryReleaseID != nil {
		if rel, err := r.releases.FindByID(*app.CanaryReleaseID); err == nil && rel.ContainerID != "" {
			targets = append(targets, target{rel.ContainerID, node.CanaryAlias(app)})
		}
	}
	for _, t := range targets {
		if attached {
			_ = eng.NetworkConnect(ctx, node.AppNetwork, t.id, []string{t.alias})
		} else {
			_ = eng.NetworkDisconnect(ctx, node.AppNetwork, t.id, true)
		}
	}
	return nil
}

// reconcileServiceIngress ensures the central gateway can reach a cluster app's
// service VIP for public ingress. The app's service is already on the shared
// ingress overlay (deployService attaches it); this joins the central gateway to
// the same overlay. Attach-only and best-effort.
func (r *ProxyNetworkReconciler) reconcileServiceIngress(ctx context.Context, app *models.Application, attached bool) error {
	if !attached {
		// A single app losing its route must not detach the shared gateway — every
		// other clustered app still needs it, and it being attached is harmless (the
		// ingress overlay carries only gateway↔VIP traffic).
		return nil
	}
	return r.ReconcileIngressGateway(ctx)
}

// ReconcileIngressGateway joins the manager's central Goma gateway to the shared
// cluster ingress overlay (creating it if needed), so public traffic can reach
// every clustered app's service VIP. It re-asserts the attachment that a compose
// recreate of the gateway (docker compose up -d) silently drops, so it is meant
// to run at worker startup and on cluster refresh as well as on each service-app
// route change or deploy. No-op when the manager is not a swarm manager or the
// gateway container is not up yet; idempotent.
func (r *ProxyNetworkReconciler) ReconcileIngressGateway(ctx context.Context) error {
	mgr, err := r.clients.For(0) // the central gateway runs on the manager (local)
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

// centralGatewayContainer resolves the compose-managed central gateway container
// on the manager engine. The role label (io.miabi.role=gateway) is authoritative:
// it survives a non-default compose project name, which the container name does
// not. Returns "" (no error) when the gateway is not running, which the caller
// treats as a retryable no-op.
//
// The name fallback exists only for a stack deployed before examples/compose/compose.yaml
// carried platform labels. It is load-bearing during that upgrade window — an
// unlabeled gateway must still be found, or clustered apps lose public ingress —
// so it warns rather than failing, and says what to do about it.
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
