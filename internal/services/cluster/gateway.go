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

package cluster

import (
	"context"
	"errors"
	"maps"
	"net/netip"
	"regexp"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/node"
)

// Errors returned by SetGateway.
var (
	ErrGatewayNeedsSwarm       = errors.New("only a swarm cluster other than the default one has a gateway of its own")
	ErrGatewayNodeNotInCluster = errors.New("the gateway node must belong to the cluster")
	ErrGatewayNodeNoGateway    = errors.New("the gateway node must run its own gateway: set its connectivity to edge gateway")
	ErrInvalidIngressIP        = errors.New("the public IPv4 address is not a valid IPv4 address")
	ErrInvalidIngressIPv6      = errors.New("the public IPv6 address is not a valid, global IPv6 address")
	ErrInvalidIngressHostname  = errors.New("the ingress hostname must be a DNS name such as lb.eu-central.example.com")
)

var hostnamePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// GatewayPatch sets the node serving a swarm cluster's routes and, when Address is set, the address its DNS
// records point at.
type GatewayPatch struct {
	ServerID uint
	Address  *models.PublicAddress
}

// SetGatewayListener is told when a cluster's gateway moves, so the cluster's routes follow it.
func (s *Service) SetGatewayListener(fn func(ctx context.Context, clusterID uint)) {
	s.gatewayListener = fn
}

// OwnGateway returns a cluster that its own ingress node serves: a swarm outside the default cluster, whose overlay
// the control-plane gateway cannot join. Read from the stored mode, so routes stay put while a manager is offline.
func (s *Service) OwnGateway(clusterID uint) (*models.Cluster, bool) {
	if s.store == nil || s.isDefault(clusterID) {
		return nil, false
	}
	c, err := s.store.FindByID(clusterID)
	if err != nil || c.IsDefault || c.Mode != models.ClusterModeSwarm {
		return nil, false
	}
	return c, true
}

// SetGateway picks the node that serves a swarm cluster's routes and the address its DNS records point at.
func (s *Service) SetGateway(ctx context.Context, clusterID uint, p GatewayPatch) (*models.Cluster, error) {
	c, err := s.Cluster(clusterID)
	if err != nil {
		return nil, err
	}
	if _, ok := s.OwnGateway(c.ID); !ok {
		return nil, ErrGatewayNeedsSwarm
	}
	srv, err := s.nodes.Get(p.ServerID)
	if err != nil || srv.ClusterID != c.ID {
		return nil, ErrGatewayNodeNotInCluster
	}
	if srv.Connectivity != models.ConnectivityEdgeGateway {
		return nil, ErrGatewayNodeNoGateway
	}
	cols := map[string]any{"ingress_server_id": srv.ID}
	if p.Address != nil {
		addr, err := normalizeIngress(*p.Address)
		if err != nil {
			return nil, err
		}
		maps.Copy(cols, ingressColumns(addr))
	}
	if err := s.store.UpdateColumns(c.ID, cols); err != nil {
		return nil, err
	}
	s.AttachGateway(ctx, c.ID)
	s.ResyncRoutes(ctx, c.ID)
	return s.Cluster(c.ID)
}

// normalizeIngress validates a cluster's public address: an IPv4 and an IPv6 address, and a DNS name for records
// that cannot use either.
func normalizeIngress(a models.PublicAddress) (models.PublicAddress, error) {
	var out models.PublicAddress
	if v4 := strings.TrimSpace(a.IPv4); v4 != "" {
		ip, err := netip.ParseAddr(v4)
		if err != nil || !ip.Is4() {
			return out, ErrInvalidIngressIP
		}
		out.IPv4 = ip.String()
	}
	if v6 := strings.TrimSpace(a.IPv6); v6 != "" {
		ip, err := netip.ParseAddr(strings.Trim(v6, "[]"))
		// A link-local or unique-local address answers only on the machine's own network, never from the internet.
		if err != nil || !ip.Is6() || ip.Is4In6() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			return out, ErrInvalidIngressIPv6
		}
		out.IPv6 = ip.String()
	}
	out.Hostname = models.NormalizeHost(a.Hostname)
	if out.Hostname != "" && !hostnamePattern.MatchString(out.Hostname) {
		return out, ErrInvalidIngressHostname
	}
	return out, nil
}

func ingressColumns(a models.PublicAddress) map[string]any {
	return map[string]any{"ingress_ip": a.IPv4, "ingress_ipv6": a.IPv6, "ingress_hostname": a.Hostname}
}

// ResyncRoutes re-syncs a cluster's routes in the background, after the gateway serving them changed.
func (s *Service) ResyncRoutes(ctx context.Context, clusterID uint) {
	if s.gatewayListener != nil {
		go s.gatewayListener(context.WithoutCancel(ctx), clusterID)
	}
}

// ConfirmGateway records that a node converted from port-forward serves its apps through its own gateway.
func (s *Service) ConfirmGateway(clusterID uint) error {
	c, err := s.Cluster(clusterID)
	if err != nil {
		return err
	}
	return s.store.UpdateColumns(c.ID, map[string]any{"legacy_ingress": false, "ingress_server_id": c.ManagerServerID})
}

// AttachGateway joins a swarm cluster's ingress-node gateway to the cluster's ingress overlay, which is how it
// reaches apps on the other nodes. Every refresh re-asserts it, in case the attachment was lost.
func (s *Service) AttachGateway(ctx context.Context, clusterID uint) {
	c, ok := s.OwnGateway(clusterID)
	if !ok {
		return
	}
	srv, err := s.nodes.Get(c.IngressNode())
	if err != nil {
		return
	}
	dc, err := s.clients.For(srv.ID)
	if err != nil {
		return
	}
	s.attachGateway(ctx, c, dc, edgegateway.ContainerNameFor(srv))
}

// AttachNodeGateway joins a node's freshly started gateway to its cluster's ingress overlay when the node is the
// ingress node of a swarm cluster other than the default one. It reads the stored cluster rather than the refreshed
// swarm state, which still lags when an agent has just reconnected.
func (s *Service) AttachNodeGateway(ctx context.Context, dc docker.Client, srv *models.Server, container string) {
	c, ok := s.OwnGateway(srv.ClusterID)
	if !ok || c.IngressNode() != srv.ID {
		return
	}
	s.attachGateway(ctx, c, dc, container)
}

func (s *Service) attachGateway(ctx context.Context, c *models.Cluster, dc docker.Client, container string) {
	if mgr, err := s.Manager(ctx, c.ID); err == nil {
		ensureIngressOverlay(ctx, mgr)
	}
	// A gateway not deployed yet has nothing to attach; its deploy attaches it.
	if err := dc.NetworkConnect(ctx, node.IngressOverlay, container, nil); err != nil && !cerrdefs.IsNotFound(err) {
		logger.Warn("could not join the cluster gateway to its ingress overlay", "cluster", c.Name, "container", container, "error", err)
	}
}
