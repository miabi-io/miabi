// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/services/node"
)

// IngressAddress is where a cluster's public DNS records point: its gateway, or a load balancer in front of it.
// Id 0 is the default cluster.
func (s *Service) IngressAddress(clusterID uint) (ip, hostname string) {
	if s.store == nil {
		return "", ""
	}
	c, err := s.store.FindByID(clusterID)
	if err != nil {
		return "", ""
	}
	return c.IngressIP, c.IngressHostname
}

// IsDefaultCluster reports whether a cluster id names the default cluster, whose apps the control plane serves.
func (s *Service) IsDefaultCluster(clusterID uint) bool { return s.isDefault(clusterID) }

// LearnIngressIP fills a cluster's empty public address with the public source IP its ingress node's agent
// connected from, so an admin need not type it. A private source address says nothing behind NAT and is ignored.
func (s *Service) LearnIngressIP(serverID uint, remoteAddr string) {
	ip := node.PublicIP(remoteAddr)
	if ip == "" || s.store == nil {
		return
	}
	srv, err := s.nodes.Get(serverID)
	if err != nil || srv.IsLocal {
		return
	}
	c, err := s.store.FindByID(srv.ClusterID)
	if err != nil || c.IsDefault || c.IngressNode() != srv.ID || c.IngressIP != "" || c.IngressHostname != "" {
		return
	}
	if err := s.store.UpdateColumns(c.ID, map[string]any{"ingress_ip": ip}); err != nil {
		logger.Warn("failed to store a cluster's learned public address", "cluster", c.Name, "error", err)
		return
	}
	logger.Info("learned a cluster's public address from its ingress node", "cluster", c.Name, "ingress_ip", ip)
	s.ResyncRoutes(context.Background(), c.ID)
}
