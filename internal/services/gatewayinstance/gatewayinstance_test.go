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

package gatewayinstance

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/redis/go-redis/v9"
)

type fakeBundles struct{ host string }

func (f *fakeBundles) ClusterBundle(*models.Cluster) ([]proxy.RenderedRoute, []proxy.RenderedMiddleware, error) {
	return []proxy.RenderedRoute{{Name: "web", Hosts: []string{f.host}}}, nil, nil
}

func (f *fakeBundles) NodeBundle(uint) ([]proxy.RenderedRoute, []proxy.RenderedMiddleware, error) {
	return []proxy.RenderedRoute{{Name: "web", Hosts: []string{f.host}}}, nil, nil
}

type fakeKeys struct{}

func (fakeKeys) ClusterKey(*models.Cluster) string { return "cluster-key" }
func (fakeKeys) NodeKey(uint) string               { return "node-key" }

type fakeClusters struct{}

func (fakeClusters) FindByID(id uint) (*models.Cluster, error) {
	return &models.Cluster{ID: id, Name: "safari-01", Mode: models.ClusterModeSwarm, IngressServerID: 5}, nil
}

func newService(t *testing.T, bundles *fakeBundles) (*Service, *time.Time) {
	t.Helper()
	mr := miniredis.RunT(t)
	s := New(redis.NewClient(&redis.Options{Addr: mr.Addr()}), bundles, fakeKeys{}, fakeClusters{})
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}

func TestStatusJudgesEachGatewayAgainstTheConfigItWouldGetNow(t *testing.T) {
	bundles := &fakeBundles{host: "a.example.com"}
	s, now := newService(t, bundles)
	ctx := context.Background()
	current := proxy.BundleChecksum([]proxy.RenderedRoute{{Name: "web", Hosts: []string{"a.example.com"}}}, nil, "cluster-key")

	s.Record(ctx, 3, Instance{Gateway: "safari-01", NodeID: 3, Endpoint: EndpointCluster, Checksum: current})
	s.Record(ctx, 3, Instance{Gateway: "safari-02", NodeID: 4, Endpoint: EndpointCluster, Checksum: "old"})
	*now = now.Add(5 * time.Minute)
	s.Record(ctx, 3, Instance{Gateway: "safari-03", NodeID: 5, Endpoint: EndpointCluster, Checksum: current})
	s.Record(ctx, 3, Instance{Gateway: "ip:203.0.113.9", Endpoint: EndpointCluster, Checksum: "old"})
	s.Record(ctx, 3, Instance{Gateway: "safari-04", NodeID: 6, Endpoint: EndpointNode, Checksum: current})

	views, err := s.Status(ctx, 3)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	got := map[string]string{}
	for _, v := range views {
		got[v.Gateway] = v.Status
	}
	want := map[string]string{
		"safari-01":      StatusOffline,
		"safari-02":      StatusOffline,
		"safari-03":      StatusInSync,
		"ip:203.0.113.9": StatusSyncing,
		"safari-04":      StatusNotServing,
	}
	for g, st := range want {
		if got[g] != st {
			t.Errorf("%s: status %q, want %q", g, got[g], st)
		}
	}

	bundles.host = "b.example.com"
	views, _ = s.Status(ctx, 3)
	for _, v := range views {
		if v.Gateway == "safari-03" && v.Status != StatusSyncing {
			t.Errorf("after a config change safari-03 is %q, want syncing", v.Status)
		}
	}
}

func TestRecentNodesSkipsOfflineAndUnidentifiedGateways(t *testing.T) {
	s, now := newService(t, &fakeBundles{host: "a.example.com"})
	ctx := context.Background()
	s.Record(ctx, 3, Instance{Gateway: "safari-01", NodeID: 3, Endpoint: EndpointCluster})
	*now = now.Add(3 * time.Minute)
	s.Record(ctx, 3, Instance{Gateway: "safari-02", NodeID: 4, Endpoint: EndpointCluster})
	s.Record(ctx, 3, Instance{Gateway: "ip:203.0.113.9", Endpoint: EndpointCluster})

	if got := s.RecentNodes(ctx, 3); !slices.Equal(got, []uint{4}) {
		t.Errorf("RecentNodes = %v, want only node 4", got)
	}
	*now = now.Add(8 * 24 * time.Hour)
	if views, _ := s.Status(ctx, 3); len(views) != 0 {
		t.Errorf("gateways gone a week are still listed: %v", views)
	}
}

func TestNilServiceRecordsNothing(t *testing.T) {
	var s *Service
	s.Record(context.Background(), 3, Instance{Gateway: "x"})
	if views, err := s.Status(context.Background(), 3); err != nil || len(views) != 0 {
		t.Errorf("nil Status = %v, %v", views, err)
	}
	if New(nil, nil, nil, nil) != nil {
		t.Error("New without Redis should return nil")
	}
}

func TestGomaVersion(t *testing.T) {
	if GomaVersion("goma-gateway/1.2.1") != "1.2.1" || GomaVersion("curl/8") != "" {
		t.Error("GomaVersion parsed the User-Agent wrong")
	}
}

type fakeServers map[uint]models.Server

func (f fakeServers) FindByID(id uint) (*models.Server, error) {
	srv, ok := f[id]
	if !ok {
		return nil, context.Canceled
	}
	return &srv, nil
}

func TestAnAdditionalGatewayCountsAsServing(t *testing.T) {
	s, _ := newService(t, &fakeBundles{host: "a.example.com"})
	s.SetServers(fakeServers{
		6: {ID: 6, ClusterID: 3, Connectivity: models.ConnectivityEdgeGateway, AdditionalGateway: true},
		7: {ID: 7, ClusterID: 3, Connectivity: models.ConnectivityCluster},
	})
	ctx := context.Background()
	current := proxy.BundleChecksum([]proxy.RenderedRoute{{Name: "web", Hosts: []string{"a.example.com"}}}, nil, "cluster-key")
	s.Record(ctx, 3, Instance{Gateway: "extra", NodeID: 6, Endpoint: EndpointCluster, Checksum: current})
	s.Record(ctx, 3, Instance{Gateway: "former", NodeID: 7, Endpoint: EndpointCluster, Checksum: current})
	views, _ := s.Status(ctx, 3)
	got := map[string]string{}
	for _, v := range views {
		got[v.Gateway] = v.Status
	}
	if got["extra"] != StatusInSync || got["former"] != StatusNotServing {
		t.Errorf("statuses = %v, want extra in sync and former not serving", got)
	}
}
