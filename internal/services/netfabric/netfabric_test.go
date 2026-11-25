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

package netfabric

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type fakeEngine struct {
	docker.Client
	nets []docker.Network
}

func (f fakeEngine) ListNetworks(context.Context) ([]docker.Network, error) { return f.nets, nil }

type fakeClients map[uint]docker.Client

func (f fakeClients) For(id uint) (docker.Client, error) {
	if c, ok := f[id]; ok {
		return c, nil
	}
	return nil, errors.New("node is offline")
}

type fakeNodes []models.Server

func (f fakeNodes) List(context.Context) ([]models.Server, error) { return f, nil }

type fakeClusters []models.Cluster

func (f fakeClusters) List() ([]models.Cluster, error) { return f, nil }

type fakeLedger []models.NetworkAllocation

func (f fakeLedger) All() ([]models.NetworkAllocation, error) { return f, nil }

type fakeModels []models.Network

func (f fakeModels) All() ([]models.Network, error) { return f, nil }

type fakeStacks []string

func (f fakeStacks) DockerNetworks() ([]string, error) { return f, nil }

func kinds(fs []Finding) map[string]Finding {
	out := map[string]Finding{}
	for _, f := range fs {
		out[f.Kind+"/"+f.Network] = f
	}
	return out
}

// The drift found on the safari test cluster on 2026-11-24, as a fixture.
func TestCheckReportsTheDriftFoundOnARemoteSwarm(t *testing.T) {
	ipv6 := true
	edge := fakeEngine{nets: []docker.Network{
		{Name: "miabi-proxy", Driver: "bridge", Subnet: "172.19.0.0/16"},
		{Name: "miabi", Driver: "bridge", Subnet: "172.20.0.0/16", Labels: map[string]string{"io.miabi.role": "control-plane"}},
		{Name: "mb-ws4-shadow", Driver: "bridge", Subnet: "10.64.20.0/24"},
		{Name: "mb-ws9-gone", Driver: "bridge", Subnet: "10.64.70.0/24"},
		{Name: "mb-ws1-drift", Driver: "bridge", Subnet: "10.64.5.0/24"},
		{Name: "mb-ws2-docker", Driver: "bridge", Subnet: "172.30.0.0/16"},
		{Name: "mb-ws3-old", Driver: "bridge", Subnet: "10.64.0.0/24"},
		{Name: "mb-stack-ws5-shop", Driver: "bridge", Subnet: "10.64.9.0/24"},
	}}
	manager := fakeEngine{nets: []docker.Network{{Name: "miabi-ingress", Driver: "overlay", Subnet: "10.0.1.0/24"}}}
	s := New(
		fakeNodes{
			{ID: 3, Name: "safari-01", ClusterID: 3, Connectivity: models.ConnectivityEdgeGateway},
			{ID: 4, Name: "safari-02", ClusterID: 3, Connectivity: models.ConnectivityCluster},
		},
		fakeClients{3: edge},
		fakeClusters{{ID: 3, Name: "safari-01", Mode: models.ClusterModeSwarm}},
		fakeLedger{
			{DockerName: "mb-ws1-drift", Subnet: "10.64.4.0/24", Kind: models.NetAllocKindWorkspace},
			{DockerName: "mb-stack-ws5-shop", Subnet: "10.64.9.0/24", Kind: models.NetAllocKindStack},
			{DockerName: "reserved:10.64.0.0/24", Subnet: "10.64.0.0/24", Kind: models.NetAllocKindReserved},
		},
		fakeModels{
			{DockerName: "mb-ws4-shadow", Driver: "overlay"},
			{DockerName: "mb-ws1-drift", Driver: "bridge"},
			{DockerName: "mb-ws2-docker", Driver: "bridge"},
			{DockerName: "mb-ws3-old", Driver: "bridge"},
		},
		fakeStacks{"mb-stack-ws5-shop"},
		func() Expected {
			return Expected{
				Proxy:       docker.NetworkSpec{Name: "miabi-proxy", Subnet: "10.63.0.0/16", EnableIPv6: &ipv6},
				LegacyProxy: "miabi", IngressName: "miabi-ingress", PoolCIDR: "10.64.0.0/12",
				ManagerOf:    func(context.Context, uint) (docker.Client, error) { return manager, nil },
				IsSwarm:      func(uint) bool { return true },
				LegacyIsOurs: func(n docker.Network) bool { return n.Labels["io.miabi.role"] == "control-plane" },
			}
		},
	)

	rep, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var gw, offline NodeReport
	for _, n := range rep.Nodes {
		switch n.NodeID {
		case 3:
			gw = n
		case 4:
			offline = n
		}
	}
	if offline.Reachable || offline.Error == "" {
		t.Errorf("offline node = %+v, want unreachable with its error", offline)
	}
	got := kinds(gw.Findings)
	for key, sev := range map[string]string{
		"proxy-subnet/miabi-proxy":     SeverityWarning,
		"proxy-ipv6/miabi-proxy":       SeverityInfo,
		"legacy-proxy/miabi":           SeverityWarning,
		"shadow-bridge/mb-ws4-shadow":  SeverityError,
		"orphan/mb-ws9-gone":           SeverityWarning,
		"subnet-mismatch/mb-ws1-drift": SeverityWarning,
		"outside-pool/mb-ws2-docker":   SeverityInfo,
	} {
		if f, ok := got[key]; !ok || f.Severity != sev {
			t.Errorf("%s: finding %+v present=%v, want severity %s", key, f, ok, sev)
		}
	}
	for _, clean := range []string{"mb-stack-ws5-shop", "mb-ws3-old"} {
		for key := range got {
			if len(key) > len(clean) && key[len(key)-len(clean):] == clean {
				t.Errorf("%s matches its records but was flagged: %s", clean, key)
			}
		}
	}
	if len(rep.Reserved) != 1 || rep.Reserved[0].Network != "mb-ws3-old" || rep.Reserved[0].NodeName != "safari-01" {
		t.Errorf("reserved = %+v, want 10.64.0.0/24 resolved to mb-ws3-old on safari-01", rep.Reserved)
	}
	if len(rep.Clusters) != 1 || kinds(rep.Clusters[0].Findings)["ingress-outside-pool/miabi-ingress"].Severity != SeverityWarning {
		t.Errorf("clusters = %+v, want the ingress overlay flagged as outside the pool", rep.Clusters)
	}
}

func TestACleanNodeHasNoFindings(t *testing.T) {
	ipv6 := false
	s := New(
		fakeNodes{{ID: 1, Name: "manager", IsLocal: true}},
		fakeClients{1: fakeEngine{nets: []docker.Network{{Name: "miabi-proxy", Driver: "bridge", Subnet: "10.63.0.0/16"}}}},
		fakeClusters{{ID: 1, Name: "default", IsDefault: true}},
		fakeLedger{}, fakeModels{}, fakeStacks{},
		func() Expected {
			return Expected{Proxy: docker.NetworkSpec{Name: "miabi-proxy", Subnet: "10.63.0.0/16", EnableIPv6: &ipv6},
				LegacyProxy: "miabi", IngressName: "miabi-ingress", PoolCIDR: "10.64.0.0/12", IsSwarm: func(uint) bool { return false }}
		},
	)
	rep, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Nodes) != 1 || !rep.Nodes[0].Reachable || len(rep.Nodes[0].Findings) != 0 || len(rep.Clusters) != 0 {
		t.Errorf("report = %+v, want one reachable node without findings", rep)
	}
}
