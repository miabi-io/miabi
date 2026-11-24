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
	"fmt"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"gopkg.in/yaml.v3"
)

const clusterProviderPath = "/api/v1/providers/"

// GatewayIDHeader carries the node a gateway runs on; the provider checks it against the cluster before trusting it.
const GatewayIDHeader = "X-Miabi-Gateway"

// ClusterGateways resolves the cluster a node's gateway serves. Implemented by services/cluster.
type ClusterGateways interface {
	ServedCluster(srv *models.Server) (*models.Cluster, bool)
	GatewayToken(clusterID uint) (token, name string, err error)
}

func (s *Service) SetClusterGateways(c ClusterGateways) { s.clusters = c }

// The token and key must match the endpoint polled, or the gateway fetches a bundle it cannot decrypt.
type gatewayCredential struct {
	token string
	key   string
}

func (s *Service) servedCluster(srv *models.Server) (*models.Cluster, bool) {
	// Miabi never redeploys an imported gateway, so it keeps the endpoint and central key it was installed with.
	if s.clusters == nil || srv == nil || isManager(srv) || srv.GatewayImported {
		return nil, false
	}
	return s.clusters.ServedCluster(srv)
}

func (s *Service) nodeEndpoint(srv *models.Server) string {
	return fmt.Sprintf("%s/api/v1/provider/%s", s.controlURL, srv.Name)
}

func (s *Service) providerEndpoint(srv *models.Server) string {
	if c, ok := s.servedCluster(srv); ok {
		return s.controlURL + clusterProviderPath + c.Name
	}
	return s.nodeEndpoint(srv)
}

// A customised config still naming the node endpoint keeps the node credential, so it keeps working.
func (s *Service) pollsClusterEndpoint(srv *models.Server) bool {
	custom := strings.TrimSpace(srv.GatewayConfigYAML)
	if custom == "" {
		return true
	}
	var cfg gomaConfig
	if err := yaml.Unmarshal([]byte(custom), &cfg); err != nil {
		return false
	}
	p := cfg.Gateway.Providers.HTTP
	return p != nil && p.Enabled && strings.Contains(p.Endpoint, clusterProviderPath)
}

// clusterPolled is the cluster whose endpoint this gateway polls, if any.
func (s *Service) clusterPolled(srv *models.Server) (*models.Cluster, bool) {
	c, ok := s.servedCluster(srv)
	if !ok || !s.pollsClusterEndpoint(srv) {
		return nil, false
	}
	return c, true
}

// ProviderScope names the endpoint the gateway polls: the cluster's name, or "" for its own node endpoint.
func (s *Service) ProviderScope(srv *models.Server) string {
	if c, ok := s.clusterPolled(srv); ok {
		return c.Name
	}
	return ""
}

// EffectiveConfigKey is the key the gateway's bundle is encrypted under.
func (s *Service) EffectiveConfigKey(srv *models.Server, central string) string {
	if c, ok := s.clusterPolled(srv); ok {
		return ClusterConfigKey(c, central)
	}
	return ConfigKey(srv, central)
}

func (s *Service) credential(srv *models.Server, nodeToken string) gatewayCredential {
	node := gatewayCredential{token: nodeToken, key: ConfigKey(srv, s.configEncKey)}
	c, ok := s.clusterPolled(srv)
	if !ok {
		return node
	}
	tok, name, err := s.clusters.GatewayToken(c.ID)
	if err != nil || tok == "" || name != c.Name {
		logger.Warn("edgegateway: cluster gateway token unavailable; the gateway keeps its node credential",
			"node", srv.Name, "cluster", c.Name, "error", err)
		return node
	}
	return gatewayCredential{token: tok, key: ClusterConfigKey(c, s.configEncKey)}
}

// LegacyProviderCluster names the cluster a node serves while its customised config still polls the node endpoint.
func (s *Service) LegacyProviderCluster(srv *models.Server) string {
	c, ok := s.servedCluster(srv)
	if !ok || s.effectiveFileProvider(srv) || s.pollsClusterEndpoint(srv) {
		return ""
	}
	return c.Name
}
