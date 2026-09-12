// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"errors"
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
		{"bad ip", 5, GatewayPatch{ServerID: 11, IP: "10.0.0"}, ErrInvalidIngressIP},
		{"url as hostname", 5, GatewayPatch{ServerID: 11, Hostname: "https://lb.example.com"}, ErrInvalidIngressHostname},
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
	c, err := s.SetGateway(context.Background(), 5, GatewayPatch{ServerID: 11, IP: " 192.0.2.10 ", Hostname: "LB.eu-east.example.com."})
	if err != nil {
		t.Fatal(err)
	}
	if c.IngressServerID != 11 || c.IngressIP != "192.0.2.10" || c.IngressHostname != "lb.eu-east.example.com" {
		t.Errorf("cluster = node %d, ip %q, hostname %q", c.IngressServerID, c.IngressIP, c.IngressHostname)
	}
	if _, ok := s.OwnGateway(1); ok {
		t.Error("the default cluster reported a gateway of its own")
	}
	if got, ok := s.OwnGateway(5); !ok || got.IngressNode() != 11 {
		t.Errorf("OwnGateway(5) = %+v, %v", got, ok)
	}
}
