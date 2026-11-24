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
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

type fakeClusterGateways struct {
	cluster *models.Cluster
	token   string
}

func (f fakeClusterGateways) ServedCluster(srv *models.Server) (*models.Cluster, bool) {
	if f.cluster == nil || srv.ClusterID != f.cluster.ID {
		return nil, false
	}
	return f.cluster, true
}

func (f fakeClusterGateways) GatewayToken(uint) (string, string, error) {
	return f.token, f.cluster.Name, nil
}

func newClusterGatewayService() *Service {
	s := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")
	s.SetConfigEncryptionKey(central)
	s.SetClusterGateways(fakeClusterGateways{cluster: &models.Cluster{ID: 3, Name: "safari-01"}, token: "mbgw_cluster"})
	return s
}

// Token, key and endpoint switch together, or the gateway would fetch a bundle it cannot decrypt.
func TestClusterIngressGatewayPollsTheClusterWithTheClusterCredential(t *testing.T) {
	s := newClusterGatewayService()
	srv := &models.Server{ID: 7, Name: "edge-7", ClusterID: 3, Address: "10.0.0.7"}

	cfg := s.RenderConfig(srv)
	if !strings.Contains(cfg, "https://miabi.example.com/api/v1/providers/safari-01") {
		t.Fatalf("rendered config does not poll the cluster endpoint:\n%s", cfg)
	}
	if !strings.Contains(cfg, GatewayIDHeader+": edge-7") {
		t.Errorf("rendered config does not name the gateway's node:\n%s", cfg)
	}
	env := s.gatewayEnv(srv, "mbn_node", "")
	if got := envValue(env, apiKeyEnv); got != "mbgw_cluster" {
		t.Errorf("%s = %q, want the cluster token", apiKeyEnv, got)
	}
	if got := envValue(env, reloadTokenEnv); got != ReloadToken("mbgw_cluster") {
		t.Errorf("reload token not derived from the cluster token")
	}
	if got := envValue(env, configEncryptionKeyEnv); got != ClusterConfigKey(&models.Cluster{ID: 3}, central) {
		t.Errorf("config key = %q, want the cluster key", got)
	}
	if s.ProviderScope(srv) != "safari-01" || s.LegacyProviderCluster(srv) != "" {
		t.Errorf("scope = %q, legacy = %q; want safari-01 and no drift", s.ProviderScope(srv), s.LegacyProviderCluster(srv))
	}
}

func TestNodeCredentialStaysWhereTheClusterEndpointIsNotPolled(t *testing.T) {
	s := newClusterGatewayService()
	custom := "version: 2\ngateway:\n  providers:\n    http:\n      enabled: true\n      endpoint: https://miabi.example.com/api/v1/provider/edge-7\n"
	for _, tc := range []struct {
		name       string
		srv        *models.Server
		wantLegacy string
	}{
		{"a node outside the served cluster", &models.Server{ID: 8, Name: "edge-8", ClusterID: 9}, ""},
		{"an imported gateway", &models.Server{ID: 7, Name: "edge-7", ClusterID: 3, GatewayImported: true}, ""},
		{"a customised config on the node endpoint", &models.Server{ID: 7, Name: "edge-7", ClusterID: 3, GatewayConfigYAML: custom}, "safari-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := s.gatewayEnv(tc.srv, "mbn_node", "")
			if got := envValue(env, apiKeyEnv); got != "mbn_node" {
				t.Errorf("%s = %q, want the node token", apiKeyEnv, got)
			}
			if got := envValue(env, configEncryptionKeyEnv); got != ConfigKey(tc.srv, central) {
				t.Errorf("config key = %q, want the node's own", got)
			}
			if got := s.LegacyProviderCluster(tc.srv); got != tc.wantLegacy {
				t.Errorf("legacy provider cluster = %q, want %q", got, tc.wantLegacy)
			}
		})
	}
}

// An additional gateway shares the bundle but orders no certificates: its HTTP-01 could only fail.
func TestAdditionalGatewayRunsWithoutACME(t *testing.T) {
	s := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")
	c := &models.Cluster{ID: 3, Name: "safari-01", IngressServerID: 7}
	s.SetClusterGateways(fakeClusterGateways{cluster: c, token: "mbgw_cluster"})

	gatewayNode := s.RenderConfig(&models.Server{ID: 7, Name: "edge-7", ClusterID: 3})
	additional := s.RenderConfig(&models.Server{ID: 8, Name: "edge-8", ClusterID: 3})
	if !strings.Contains(gatewayNode, "certManager") {
		t.Errorf("the gateway node lost its ACME provider:\n%s", gatewayNode)
	}
	if strings.Contains(additional, "certManager") || !strings.Contains(additional, "/api/v1/providers/safari-01") {
		t.Errorf("additional gateway config should poll the cluster without ACME:\n%s", additional)
	}
}
