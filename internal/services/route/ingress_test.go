// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package route

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// serverRow is a sqlite-friendly stand-in for models.Server, whose UIDModel carries a Postgres-only
// gen_random_uuid() default. It mirrors only the columns routing reads, under the same table name.
type serverRow struct {
	ID           uint `gorm:"primaryKey"`
	IsLocal      bool
	Connectivity string
	Address      string
	ClusterID    uint
}

func (serverRow) TableName() string { return "servers" }

const (
	localServer       = 1
	memberServer      = 2
	edgeServer        = 3
	defaultEdgeServer = 4

	defaultCluster = 1
	edgeCluster    = 3
)

func newIngressService(t *testing.T) *Service {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_")
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&serverRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	servers := []serverRow{
		{ID: localServer, IsLocal: true, Connectivity: string(models.ConnectivityCluster), ClusterID: defaultCluster},
		{ID: memberServer, Connectivity: string(models.ConnectivityCluster), ClusterID: defaultCluster},
		{ID: edgeServer, Connectivity: string(models.ConnectivityEdgeGateway), ClusterID: edgeCluster},
		{ID: defaultEdgeServer, Connectivity: string(models.ConnectivityEdgeGateway), ClusterID: defaultCluster},
	}
	if err := db.Create(&servers).Error; err != nil {
		t.Fatalf("seed servers: %v", err)
	}
	s := &Service{servers: repositories.NewServerRepository(db)}
	s.SetCluster(clusterAddresses())
	return s
}

func clusterAddresses() fakeCluster {
	return fakeCluster{defaultID: defaultCluster, ingress: map[uint]string{defaultCluster: "198.51.100.1", edgeCluster: "203.0.113.3"}}
}

// Every upstream is an alias: whichever gateway serves an app shares a network with it.
func TestGatewayBackends(t *testing.T) {
	s := newIngressService(t)
	app := &models.Application{ID: 7, ServerID: memberServer, Alias: "mb-app-tok-7"}
	if got := s.displayBackends(app, 8080); len(got) != 1 || got[0] != "http://mb-app-tok-7:8080" {
		t.Errorf("displayBackends = %v, want the alias", got)
	}
	rel := uint(9)
	canary := *app
	canary.CanaryReleaseID, canary.CanaryWeight = &rel, 20
	if got := gatewayBackends(&canary, 8080); len(got) != 2 || got[0].Weight != 80 || got[1].Weight != 20 {
		t.Errorf("gatewayBackends = %+v, want an 80/20 stable+canary split", got)
	}
	svc := &models.Application{ID: 8, RuntimeKind: models.RuntimeService, Alias: "mb-app-tok-8"}
	if got := gatewayBackends(svc, 80); len(got) != 1 || got[0].Endpoint != "http://mb-app-tok-8:80" {
		t.Errorf("service backends = %+v, want its VIP alias", got)
	}
}

// DNS points at the public address of the cluster whose gateway serves the app. A service app is always served by
// the central gateway, and so is every app of the default cluster, even on a node that still runs a gateway.
func TestGatewayServerAndDNSTarget(t *testing.T) {
	s := newIngressService(t)

	tests := []struct {
		name     string
		app      *models.Application
		wantEdge bool
		wantIP   string
	}{
		{"container app on an edge node", &models.Application{ServerID: edgeServer, ClusterID: edgeCluster}, true, "203.0.113.3"},
		{"service app on an edge node", &models.Application{ServerID: edgeServer, RuntimeKind: models.RuntimeService}, false, "198.51.100.1"},
		{"container app on the local node", &models.Application{ServerID: localServer, ClusterID: defaultCluster}, false, "198.51.100.1"},
		{"container app on a cluster member", &models.Application{ServerID: memberServer, ClusterID: defaultCluster}, false, "198.51.100.1"},
		{"container app on a gateway node of the default cluster", &models.Application{ServerID: defaultEdgeServer, ClusterID: defaultCluster}, false, "198.51.100.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := s.gatewayServer(tt.app); got != tt.wantEdge {
				t.Errorf("gatewayServer = %v, want %v", got, tt.wantEdge)
			}
			if ip, _ := s.dnsTarget(tt.app); ip != tt.wantIP {
				t.Errorf("dnsTarget = %q, want %q", ip, tt.wantIP)
			}
		})
	}
}

// The control-plane gateway cannot join a remote swarm's overlay, so the swarm's ingress node serves every app
// in it, service apps included, whichever node they run on. Its DNS target is the cluster's own address only.
func TestRemoteSwarmIsServedByItsIngressNode(t *testing.T) {
	s := newIngressService(t)
	gw := &models.Cluster{ID: 9, Mode: models.ClusterModeSwarm, IngressServerID: edgeServer}
	addrs := clusterAddresses()
	addrs.on, addrs.gateway = true, gw
	s.SetCluster(addrs)

	for _, app := range []*models.Application{
		{ClusterID: 9, ServerID: memberServer, Alias: "mb-app-x-7"},
		{ClusterID: 9, ServerID: memberServer, RuntimeKind: models.RuntimeService, Alias: "mb-app-x-8"},
	} {
		if id, ok := s.gatewayServer(app); !ok || id != edgeServer {
			t.Errorf("app %s: gatewayServer = %d, %v, want the ingress node", app.Alias, id, ok)
		}
		if ip, _ := s.dnsTarget(app); ip != "" {
			t.Errorf("app %s: dnsTarget = %q, want none until the cluster has a public address", app.Alias, ip)
		}
		if got := s.displayBackends(app, 8080); len(got) != 1 || got[0] != "http://"+app.Alias+":8080" {
			t.Errorf("app %s: displayBackends = %v, want its alias", app.Alias, got)
		}
	}

	gw.IngressIP = "192.0.2.10"
	if ip, _ := s.dnsTarget(&models.Application{ClusterID: 9, ServerID: memberServer}); ip != "192.0.2.10" {
		t.Errorf("dnsTarget = %q, want the cluster's public address", ip)
	}
	if _, ok := s.gatewayServer(&models.Application{ClusterID: 1, ServerID: memberServer}); ok {
		t.Error("an app outside the swarm was handed to its gateway")
	}
}
