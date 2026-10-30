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
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

// IngressAddress is where a cluster's public DNS records point: its gateway, or a load balancer in front of it.
// Id 0 is the default cluster.
func (s *Service) IngressAddress(clusterID uint) models.PublicAddress {
	if s.store == nil {
		return models.PublicAddress{}
	}
	c, err := s.store.FindByID(clusterID)
	if err != nil {
		return models.PublicAddress{}
	}
	return c.PublicAddress()
}

// IsDefaultCluster reports whether a cluster id names the default cluster, whose apps the control plane serves.
func (s *Service) IsDefaultCluster(clusterID uint) bool { return s.isDefault(clusterID) }

// LearnIngressIP fills a cluster's empty public address with the public source IP its ingress node's agent
// connected from, so an admin need not type it. A private source address says nothing behind NAT and is ignored.
// Only one family is ever learned: the other may not reach the gateway, so adding it is left to an admin.
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
	if err != nil || c.IsDefault || c.IngressNode() != srv.ID || !c.PublicAddress().IsZero() {
		return
	}
	col := "ingress_ip"
	if strings.Contains(ip, ":") {
		col = "ingress_ipv6"
	}
	if err := s.store.UpdateColumns(c.ID, map[string]any{col: ip}); err != nil {
		logger.Warn("failed to store a cluster's learned public address", "cluster", c.Name, "error", err)
		return
	}
	logger.Info("learned a cluster's public address from its ingress node", "cluster", c.Name, "address", ip)
	s.ResyncRoutes(context.Background(), c.ID)
}
