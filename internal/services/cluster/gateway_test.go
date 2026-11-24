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
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
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
		{ID: 11, ClusterID: 5, Connectivity: models.ConnectivityEdgeGateway, SwarmNodeID: "n11"},
		{ID: 12, ClusterID: 5, Connectivity: models.ConnectivityCluster, SwarmNodeID: "n12"},
		{ID: 15, ClusterID: 5, Connectivity: models.ConnectivityEdgeGateway},
		{ID: 16, ClusterID: 5, Connectivity: models.ConnectivityEdgeGateway, SwarmNodeID: "n16"},
		{ID: 13, ClusterID: 1, Connectivity: models.ConnectivityEdgeGateway},
		{ID: 14, ClusterID: 6, Connectivity: models.ConnectivityEdgeGateway},
	}}
	s := NewService(fakeClients{local: &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "inactive"}}}, nodes)
	s.SetStore(store)
	return s
}

// Swarm state is stale right after an agent reconnects, so the attach must use the stored cluster.
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
		{"node that has not joined the swarm", 5, GatewayPatch{ServerID: 15}, ErrGatewayNodeNotSwarmMember},
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

// The old gateway may still hold the cluster token, so a move rotates it and redeploys both nodes.
func TestMovingTheGatewayLocksOutTheOldNode(t *testing.T) {
	crypto.Init("test-master-key-for-gateway-move")
	t.Cleanup(func() { crypto.Init("") })
	s := newGatewayService()
	redeployed := make(chan uint, 4)
	s.SetGatewayRedeployer(func(_ context.Context, id uint) { redeployed <- id })

	old, _, err := s.GatewayToken(5)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := s.SetGateway(context.Background(), 5, GatewayPatch{ServerID: 16}); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := s.AuthenticateGateway("eu-east", old); !errors.Is(err, ErrBadGatewayToken) {
		t.Error("the old gateway's token still authenticates after the move")
	}
	var got []uint
	for len(got) < 2 {
		select {
		case id := <-redeployed:
			got = append(got, id)
		case <-time.After(time.Second):
			t.Fatalf("redeployed %v, want the old node 11 and the new node 16", got)
		}
	}
	if !slices.Equal(got, []uint{11, 16}) {
		t.Errorf("redeployed %v, want [11 16]", got)
	}

	if _, err := s.SetGateway(context.Background(), 5, GatewayPatch{ServerID: 16}); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	select {
	case id := <-redeployed:
		t.Errorf("saving the same gateway node redeployed node %d", id)
	case <-time.After(100 * time.Millisecond):
	}
}

type connectivityCall struct {
	id uint
	c  models.ServerConnectivity
}

// Only the gateway node of a remote swarm runs a gateway; it is switched first so the cluster is never left without one.
func TestAlignConnectivityMakesOnlyTheGatewayNodeAnEdgeGateway(t *testing.T) {
	s := newGatewayService()
	nodes := s.nodes.(*fakeNodes)
	nodes.servers = append(nodes.servers,
		models.Server{ID: 17, ClusterID: 5, Connectivity: models.ConnectivityEdgeGateway, SwarmNodeID: "n17", GatewayImported: true},
		models.Server{ID: 18, ClusterID: 5, Connectivity: models.ConnectivityEdgeGateway, SwarmNodeID: "n18", AdditionalGateway: true})
	var calls []connectivityCall
	s.SetConnectivitySetter(func(_ context.Context, id uint, c models.ServerConnectivity) error {
		calls = append(calls, connectivityCall{id, c})
		return nil
	})

	if err := s.store.UpdateColumns(5, map[string]any{"ingress_server_id": uint(12)}); err != nil {
		t.Fatal(err)
	}
	// A stale caller copy must not decide which node keeps its gateway.
	s.alignConnectivity(context.Background(), &models.Cluster{ID: 5, Name: "eu-east", Mode: models.ClusterModeSwarm, ManagerServerID: 11})

	want := []connectivityCall{
		{12, models.ConnectivityEdgeGateway},
		{11, models.ConnectivityCluster},
		{16, models.ConnectivityCluster},
	}
	if !slices.Equal(calls, want) {
		t.Errorf("calls = %v, want %v (15 has not joined the swarm, 17 runs an imported gateway, 18 an additional one)", calls, want)
	}

	calls = nil
	s.alignConnectivity(context.Background(), &models.Cluster{ID: 6, Name: "solo", Mode: models.ClusterModeStandalone, ManagerServerID: 14})
	s.alignConnectivity(context.Background(), &models.Cluster{ID: 1, Name: "default", IsDefault: true, Mode: models.ClusterModeSwarm})
	if len(calls) != 0 {
		t.Errorf("standalone or default cluster changed connectivity: %v", calls)
	}
}

func TestMovingTheGatewayToAClusterMemberGivesItAGateway(t *testing.T) {
	s := newGatewayService()
	got := make(chan connectivityCall, 4)
	s.SetConnectivitySetter(func(_ context.Context, id uint, c models.ServerConnectivity) error {
		got <- connectivityCall{id, c}
		return nil
	})
	if _, err := s.SetGateway(context.Background(), 5, GatewayPatch{ServerID: 12}); err != nil {
		t.Fatalf("move to node 12: %v", err)
	}
	first := <-got
	if first != (connectivityCall{12, models.ConnectivityEdgeGateway}) {
		t.Errorf("first change = %v, want node 12 to edge-gateway", first)
	}
}

// Once the gateway node moves away from a node that was an additional gateway, that node must be demotable.
func TestAlignClearsTheAdditionalMarkOnTheGatewayNode(t *testing.T) {
	s := newGatewayService()
	nodes := s.nodes.(*fakeNodes)
	nodes.servers[0].AdditionalGateway = true
	var calls []connectivityCall
	s.SetConnectivitySetter(func(_ context.Context, id uint, c models.ServerConnectivity) error {
		calls = append(calls, connectivityCall{id, c})
		return nil
	})
	s.alignConnectivity(context.Background(), &models.Cluster{ID: 5, Name: "eu-east", Mode: models.ClusterModeSwarm, ManagerServerID: 11})
	if len(calls) == 0 || calls[0] != (connectivityCall{11, models.ConnectivityEdgeGateway}) {
		t.Errorf("calls = %v, want node 11 re-saved as edge-gateway first", calls)
	}
}

func TestServedClusterCountsAdditionalGateways(t *testing.T) {
	s := newGatewayService()
	c := &models.Cluster{ID: 5, Mode: models.ClusterModeSwarm, ManagerServerID: 11}
	for _, tt := range []struct {
		name string
		srv  models.Server
		want bool
	}{
		{"gateway node", models.Server{ID: 11, ClusterID: 5, Connectivity: models.ConnectivityEdgeGateway}, true},
		{"additional gateway", models.Server{ID: 16, ClusterID: 5, Connectivity: models.ConnectivityEdgeGateway, AdditionalGateway: true}, true},
		{"mark left on a cluster member", models.Server{ID: 16, ClusterID: 5, Connectivity: models.ConnectivityCluster, AdditionalGateway: true}, false},
		{"plain member", models.Server{ID: 12, ClusterID: 5, Connectivity: models.ConnectivityCluster}, false},
	} {
		if _, got := s.ServedCluster(&tt.srv); got != tt.want {
			t.Errorf("%s: ServedCluster = %v, want %v", tt.name, got, tt.want)
		}
	}
	if IsAdditionalGateway(c, &models.Server{ID: 11, ClusterID: 5, Connectivity: models.ConnectivityEdgeGateway, AdditionalGateway: true}) {
		t.Error("the gateway node itself counted as an additional gateway")
	}
}
