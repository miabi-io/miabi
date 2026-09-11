// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/node"
)

// Errors returned by SetGateway.
var (
	ErrGatewayNeedsSwarm       = errors.New("only a swarm cluster other than the default one has a gateway of its own")
	ErrGatewayNodeNotInCluster = errors.New("the gateway node must belong to the cluster")
	ErrGatewayNodeNoGateway    = errors.New("the gateway node must run its own gateway: set its connectivity to edge gateway")
	ErrInvalidIngressIP        = errors.New("the ingress IP is not a valid IP address")
	ErrInvalidIngressHostname  = errors.New("the ingress hostname must be a DNS name such as lb.eu-central.example.com")
)

var hostnamePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// GatewayPatch sets the node serving a swarm cluster's routes and the address its DNS records point at.
type GatewayPatch struct {
	ServerID uint
	IP       string
	Hostname string
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
	ip := strings.TrimSpace(p.IP)
	if ip != "" && net.ParseIP(ip) == nil {
		return nil, ErrInvalidIngressIP
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(p.Hostname)), ".")
	if host != "" && !hostnamePattern.MatchString(host) {
		return nil, ErrInvalidIngressHostname
	}
	if err := s.store.UpdateColumns(c.ID, map[string]any{
		"ingress_server_id": srv.ID,
		"ingress_ip":        ip,
		"ingress_hostname":  host,
	}); err != nil {
		return nil, err
	}
	s.AttachGateway(ctx, c.ID)
	if s.gatewayListener != nil {
		go s.gatewayListener(context.WithoutCancel(ctx), c.ID)
	}
	return s.Cluster(c.ID)
}

// AttachGateway joins a swarm cluster's ingress-node gateway to the cluster's ingress overlay, which is how it
// reaches apps on the other nodes. A gateway redeploy drops the attachment, so every refresh re-asserts it.
func (s *Service) AttachGateway(ctx context.Context, clusterID uint) {
	c, ok := s.OwnGateway(clusterID)
	if !ok || !s.IsSwarm(c.ID) {
		return
	}
	srv, err := s.nodes.Get(c.IngressNode())
	if err != nil {
		return
	}
	mgr, err := s.Manager(ctx, c.ID)
	if err != nil {
		return
	}
	ensureIngressOverlay(ctx, mgr)
	dc, err := s.clients.For(srv.ID)
	if err != nil {
		return
	}
	// Fails harmlessly once attached, or before the gateway container exists.
	_ = dc.NetworkConnect(ctx, node.IngressOverlay, edgegateway.ContainerNameFor(srv), nil)
}
