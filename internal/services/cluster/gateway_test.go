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
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

func newGatewayService() *Service {
	store := &memStore{
		def: models.Cluster{ID: 1, Name: "default", IsDefault: true, Mode: models.ClusterModeSwarm},
		others: map[uint]models.Cluster{
			5: {ID: 5, Name: "eu-east", Mode: models.ClusterModeSwarm, ManagerServerID: 11},
			6: {ID: 6, Name: "solo", Mode: models.ClusterModeStandalone, ManagerServerID: 14},
		},
	}
	nodes := &fakeNodes{servers: []models.Server{
		{ID: 11, ClusterID: 5, Connectivity: models.ConnectivityEdgeGateway},
		{ID: 12, ClusterID: 5, Connectivity: models.ConnectivityCluster},
		{ID: 13, ClusterID: 1, Connectivity: models.ConnectivityEdgeGateway},
		{ID: 14, ClusterID: 6, Connectivity: models.ConnectivityEdgeGateway},
	}}
	s := NewService(fakeClients{local: &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "inactive"}}}, nodes)
	s.SetStore(store)
	return s
}

// Recreating the gateway drops the overlay attachment, and right after an agent reconnects the swarm state is still
// stale, so the attach right after a deploy must decide from the stored cluster.
func TestANodeGatewayJoinsTheIngressOverlayRightAfterItStarts(t *testing.T) {
	s := newGatewayService()
	for _, tt := range []struct {
		name string
		srv  models.Server
		want bool
	}{
		{"ingress node of a remote swarm", models.Server{ID: 11, ClusterID: 5}, true},
		{"another member of that swarm", models.Server{ID: 12, ClusterID: 5}, false},
		{"standalone cluster", models.Server{ID: 14, ClusterID: 6}, false},
		{"default cluster", models.Server{ID: 13, ClusterID: 1}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gw := &fakeDocker{}
			s.AttachNodeGateway(context.Background(), gw, &tt.srv, "mb-node-gateway")
			if got := slices.Contains(gw.connected, "miabi-ingress/mb-node-gateway"); got != tt.want {
				t.Errorf("attached = %v (connected %v), want %v", got, gw.connected, tt.want)
			}
		})
	}
}

func TestSetGatewayRefusals(t *testing.T) {
	s := newGatewayService()
	ctx := context.Background()
	for _, tt := range []struct {
		name    string
		cluster uint
		patch   GatewayPatch
		want    error
	}{
		{"default cluster", 1, GatewayPatch{ServerID: 13}, ErrGatewayNeedsSwarm},
		{"standalone cluster", 6, GatewayPatch{ServerID: 14}, ErrGatewayNeedsSwarm},
		{"node of another cluster", 5, GatewayPatch{ServerID: 13}, ErrGatewayNodeNotInCluster},
		{"node without a gateway", 5, GatewayPatch{ServerID: 12}, ErrGatewayNodeNoGateway},
		{"bad ip", 5, GatewayPatch{ServerID: 11, Address: &models.PublicAddress{IPv4: "10.0.0"}}, ErrInvalidIngressIP},
		{"ipv6 as ipv4", 5, GatewayPatch{ServerID: 11, Address: &models.PublicAddress{IPv4: "2001:db8::1"}}, ErrInvalidIngressIP},
		{"ipv4 as ipv6", 5, GatewayPatch{ServerID: 11, Address: &models.PublicAddress{IPv6: "192.0.2.1"}}, ErrInvalidIngressIPv6},
		{"unique-local ipv6", 5, GatewayPatch{ServerID: 11, Address: &models.PublicAddress{IPv6: "fd00::1"}}, ErrInvalidIngressIPv6},
		{"link-local ipv6", 5, GatewayPatch{ServerID: 11, Address: &models.PublicAddress{IPv6: "fe80::1"}}, ErrInvalidIngressIPv6},
		{"url as hostname", 5, GatewayPatch{ServerID: 11, Address: &models.PublicAddress{Hostname: "https://lb.example.com"}}, ErrInvalidIngressHostname},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.SetGateway(ctx, tt.cluster, tt.patch); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSetGatewayStoresTheIngress(t *testing.T) {
	s := newGatewayService()
	c, err := s.SetGateway(context.Background(), 5, GatewayPatch{ServerID: 11, Address: &models.PublicAddress{
		IPv4: " 192.0.2.10 ", IPv6: "[2001:DB8:0::10]", Hostname: "LB.eu-east.example.com.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := models.PublicAddress{IPv4: "192.0.2.10", IPv6: "2001:db8::10", Hostname: "lb.eu-east.example.com"}
	if c.IngressServerID != 11 || c.PublicAddress() != want {
		t.Errorf("cluster = node %d, address %+v, want %+v", c.IngressServerID, c.PublicAddress(), want)
	}
	c, err = s.SetGateway(context.Background(), 5, GatewayPatch{ServerID: 11})
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicAddress() != want {
		t.Errorf("moving the gateway without an address changed it: %+v", c.PublicAddress())
	}
	if _, ok := s.OwnGateway(1); ok {
		t.Error("the default cluster reported a gateway of its own")
	}
	if got, ok := s.OwnGateway(5); !ok || got.IngressNode() != 11 {
		t.Errorf("OwnGateway(5) = %+v, %v", got, ok)
	}
}
