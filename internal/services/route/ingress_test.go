// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package route

import (
	"errors"
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
	ID             uint `gorm:"primaryKey"`
	IsLocal        bool
	Connectivity   string
	Address        string
	PublicIP       string
	PublicHostname string
}

func (serverRow) TableName() string { return "servers" }

const (
	localServer     = 1
	portFwdServer   = 2
	edgeServer      = 3
	noAddrFwdServer = 4
)

func newIngressService(t *testing.T, clusterOn bool) *Service {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_")
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&serverRow{}, &models.PortBinding{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	servers := []serverRow{
		{ID: localServer, IsLocal: true, PublicIP: "198.51.100.1"},
		{ID: portFwdServer, Connectivity: string(models.ConnectivityPortForward), Address: "10.0.0.2"},
		{ID: edgeServer, Connectivity: string(models.ConnectivityEdgeGateway), PublicIP: "203.0.113.3"},
		{ID: noAddrFwdServer, Connectivity: string(models.ConnectivityPortForward)},
	}
	if err := db.Create(&servers).Error; err != nil {
		t.Fatalf("seed servers: %v", err)
	}
	binding := &models.PortBinding{ApplicationID: 7, ServerID: portFwdServer, ContainerPort: 8080, HostPort: 30001,
		Protocol: "tcp", Status: models.PortBindingApproved}
	if err := db.Create(binding).Error; err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	s := &Service{servers: repositories.NewServerRepository(db), ports: repositories.NewPortBindingRepository(db)}
	if clusterOn {
		s.SetCluster(fakeCluster{on: true})
	}
	return s
}

func TestRenderBackendsFollowsClusterMode(t *testing.T) {
	routed := &models.Application{ID: 7, ServerID: portFwdServer, Alias: "mb-app-tok-7"}
	unrouted := &models.Application{ID: 8, ServerID: portFwdServer, Alias: "mb-app-tok-8"}

	t.Run("off: port-forward node uses its host port", func(t *testing.T) {
		s := newIngressService(t, false)
		if got := s.renderBackends(routed, 8080); len(got) != 1 || got[0].Endpoint != "http://10.0.0.2:30001" {
			t.Errorf("renderBackends = %+v, want the host-port upstream", got)
		}
		if got := s.displayBackends(routed, 8080); len(got) != 1 || got[0] != "http://10.0.0.2:30001" {
			t.Errorf("displayBackends = %v, want the host-port upstream", got)
		}
	})

	t.Run("on: port-forward node uses the alias", func(t *testing.T) {
		s := newIngressService(t, true)
		for _, app := range []*models.Application{routed, unrouted} {
			want := "http://" + app.Alias + ":8080"
			if got := s.renderBackends(app, 8080); len(got) != 1 || got[0].Endpoint != want {
				t.Errorf("app %d: renderBackends = %+v, want %s", app.ID, got, want)
			}
			if got := s.displayBackends(app, 8080); len(got) != 1 || got[0] != want {
				t.Errorf("app %d: displayBackends = %v, want %s", app.ID, got, want)
			}
		}
	})

	t.Run("on: canary split survives a leftover host port", func(t *testing.T) {
		s := newIngressService(t, true)
		rel := uint(9)
		canary := *routed
		canary.CanaryReleaseID, canary.CanaryWeight = &rel, 20
		got := s.renderBackends(&canary, 8080)
		if len(got) != 2 || got[0].Weight != 80 || got[1].Weight != 20 {
			t.Errorf("renderBackends = %+v, want an 80/20 stable+canary split", got)
		}
	})
}

func TestRequireRoutableNodeFollowsClusterMode(t *testing.T) {
	app := &models.Application{ID: 10, ServerID: noAddrFwdServer}

	if err := newIngressService(t, false).requireRoutableNode(app); !errors.Is(err, ErrNodeAddressRequired) {
		t.Errorf("off: err = %v, want ErrNodeAddressRequired", err)
	}
	t.Run("on", func(t *testing.T) {
		if err := newIngressService(t, true).requireRoutableNode(app); err != nil {
			t.Errorf("on: err = %v, want nil — the alias upstream needs no node address", err)
		}
	})
}

// Regression: a service-runtime app is always served by the central gateway, but its DNS pointed at the
// edge-gateway node it was assigned to, whose gateway has no route for it.
func TestEdgeGatewayAndDNSTarget(t *testing.T) {
	s := newIngressService(t, false)

	tests := []struct {
		name     string
		app      *models.Application
		wantEdge bool
		wantIP   string
	}{
		{"container app on an edge node", &models.Application{ServerID: edgeServer}, true, "203.0.113.3"},
		{"service app on an edge node", &models.Application{ServerID: edgeServer, RuntimeKind: models.RuntimeService}, false, "198.51.100.1"},
		{"container app on the local node", &models.Application{ServerID: localServer}, false, "198.51.100.1"},
		{"container app on a port-forward node", &models.Application{ServerID: portFwdServer}, false, "198.51.100.1"},
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
// in it, service apps included, whichever node they run on.
func TestRemoteSwarmIsServedByItsIngressNode(t *testing.T) {
	s := newIngressService(t, false)
	gw := &models.Cluster{ID: 9, Mode: models.ClusterModeSwarm, IngressServerID: edgeServer}
	s.SetCluster(fakeCluster{on: true, gateway: gw})

	for _, app := range []*models.Application{
		{ClusterID: 9, ServerID: noAddrFwdServer, Alias: "mb-app-x-7"},
		{ClusterID: 9, ServerID: noAddrFwdServer, RuntimeKind: models.RuntimeService, Alias: "mb-app-x-8"},
	} {
		if id, ok := s.gatewayServer(app); !ok || id != edgeServer {
			t.Errorf("app %s: gatewayServer = %d, %v, want the ingress node", app.Alias, id, ok)
		}
		if ip, _ := s.dnsTarget(app); ip != "203.0.113.3" {
			t.Errorf("app %s: dnsTarget = %q, want the ingress node's address", app.Alias, ip)
		}
		if err := s.requireRoutableNode(app); err != nil {
			t.Errorf("app %s: requireRoutableNode = %v, want nil", app.Alias, err)
		}
		if got := s.displayBackends(app, 8080); len(got) != 1 || got[0] != "http://"+app.Alias+":8080" {
			t.Errorf("app %s: displayBackends = %v, want its alias", app.Alias, got)
		}
	}

	gw.IngressIP = "192.0.2.10"
	if ip, _ := s.dnsTarget(&models.Application{ClusterID: 9, ServerID: noAddrFwdServer}); ip != "192.0.2.10" {
		t.Errorf("dnsTarget = %q, want the cluster's ingress address", ip)
	}
	if _, ok := s.gatewayServer(&models.Application{ClusterID: 1, ServerID: portFwdServer}); ok {
		t.Error("an app outside the swarm was handed to its gateway")
	}
}
