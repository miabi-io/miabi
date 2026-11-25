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

// Package netfabric compares the networks on each node with what Miabi expects them to be.
package netfabric

import (
	"context"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"

	nodeTimeout = 8 * time.Second
	parallel    = 4
)

// Finding is one way a node's networks differ from what Miabi expects.
type Finding struct {
	Severity string `json:"severity"`
	Kind     string `json:"kind"`
	Network  string `json:"network"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Message  string `json:"message"`
}

type NodeReport struct {
	NodeID      uint      `json:"node_id"`
	NodeName    string    `json:"node_name"`
	ClusterID   uint      `json:"cluster_id"`
	ClusterName string    `json:"cluster_name,omitempty"`
	Reachable   bool      `json:"reachable"`
	Error       string    `json:"error,omitempty"`
	Networks    int       `json:"networks"`
	Findings    []Finding `json:"findings"`
}

type ClusterReport struct {
	ClusterID   uint      `json:"cluster_id"`
	ClusterName string    `json:"cluster_name"`
	Findings    []Finding `json:"findings"`
}

// ReservedNetwork names the network that holds a reserved pool subnet on a node.
type ReservedNetwork struct {
	Subnet   string `json:"subnet"`
	Network  string `json:"network"`
	NodeID   uint   `json:"node_id"`
	NodeName string `json:"node_name"`
}

type Report struct {
	CheckedAt time.Time         `json:"checked_at"`
	Clusters  []ClusterReport   `json:"clusters"`
	Nodes     []NodeReport      `json:"nodes"`
	Reserved  []ReservedNetwork `json:"reserved"`
}

type Nodes interface {
	List(ctx context.Context) ([]models.Server, error)
}

type Clients interface {
	For(serverID uint) (docker.Client, error)
}

type Clusters interface {
	List() ([]models.Cluster, error)
}

type Ledger interface {
	All() ([]models.NetworkAllocation, error)
}

type WorkspaceNetworks interface {
	All() ([]models.Network, error)
}

type StackNetworks interface {
	DockerNetworks() ([]string, error)
}

// Expected is what every node's networks should look like; all names come from the running configuration.
type Expected struct {
	Proxy        docker.NetworkSpec
	LegacyProxy  string
	IngressName  string
	PoolCIDR     string
	ManagerOf    func(ctx context.Context, clusterID uint) (docker.Client, error)
	IsSwarm      func(clusterID uint) bool
	LegacyIsOurs func(n docker.Network) bool
}

type Service struct {
	nodes    Nodes
	clients  Clients
	clusters Clusters
	ledger   Ledger
	networks WorkspaceNetworks
	stacks   StackNetworks
	expect   func() Expected
	now      func() time.Time
}

func New(nodes Nodes, clients Clients, clusters Clusters, ledger Ledger, networks WorkspaceNetworks, stacks StackNetworks, expect func() Expected) *Service {
	return &Service{nodes: nodes, clients: clients, clusters: clusters, ledger: ledger, networks: networks, stacks: stacks, expect: expect, now: time.Now}
}

// known is what Miabi's own records say about Docker network names, gathered once per check.
type known struct {
	ledger   map[string]models.NetworkAllocation
	reserved map[string]bool
	models   map[string]models.Network
	stacks   map[string]bool
	pool     netip.Prefix
}

func (s *Service) gather(exp Expected) (*known, error) {
	k := &known{ledger: map[string]models.NetworkAllocation{}, reserved: map[string]bool{}, models: map[string]models.Network{}, stacks: map[string]bool{}}
	rows, err := s.ledger.All()
	if err != nil {
		return nil, err
	}
	for _, a := range rows {
		if a.Kind == models.NetAllocKindReserved {
			k.reserved[a.Subnet] = true
			continue
		}
		k.ledger[a.DockerName] = a
	}
	nets, err := s.networks.All()
	if err != nil {
		return nil, err
	}
	for _, n := range nets {
		k.models[n.DockerName] = n
	}
	names, err := s.stacks.DockerNetworks()
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		k.stacks[n] = true
	}
	k.pool, _ = netip.ParsePrefix(strings.TrimSpace(exp.PoolCIDR))
	return k, nil
}

func (s *Service) Check(ctx context.Context) (*Report, error) {
	exp := s.expect()
	k, err := s.gather(exp)
	if err != nil {
		return nil, err
	}
	servers, err := s.nodes.List(ctx)
	if err != nil {
		return nil, err
	}
	clusters, err := s.clusters.List()
	if err != nil {
		return nil, err
	}
	names := map[uint]string{}
	for _, c := range clusters {
		names[c.ID] = c.Label()
	}

	rep := &Report{CheckedAt: s.now().UTC(), Nodes: make([]NodeReport, len(servers)), Reserved: []ReservedNetwork{}}
	var mu sync.Mutex
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i := range servers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			nr, reserved := s.checkNode(ctx, &servers[i], exp, k)
			nr.ClusterName = names[nr.ClusterID]
			mu.Lock()
			rep.Nodes[i] = nr
			rep.Reserved = append(rep.Reserved, reserved...)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	for _, c := range clusters {
		if exp.IsSwarm != nil && exp.IsSwarm(c.ID) {
			rep.Clusters = append(rep.Clusters, s.checkIngress(ctx, &c, exp, k))
		}
	}
	sort.Slice(rep.Nodes, func(i, j int) bool {
		if rep.Nodes[i].ClusterName != rep.Nodes[j].ClusterName {
			return rep.Nodes[i].ClusterName < rep.Nodes[j].ClusterName
		}
		return rep.Nodes[i].NodeName < rep.Nodes[j].NodeName
	})
	sort.Slice(rep.Reserved, func(i, j int) bool { return rep.Reserved[i].Subnet < rep.Reserved[j].Subnet })
	return rep, nil
}

func (s *Service) checkNode(ctx context.Context, srv *models.Server, exp Expected, k *known) (NodeReport, []ReservedNetwork) {
	nr := NodeReport{NodeID: srv.ID, NodeName: srv.Label(), ClusterID: srv.ClusterID, Findings: []Finding{}}
	dc, err := s.clients.For(srv.ID)
	if err != nil {
		nr.Error = err.Error()
		return nr, nil
	}
	ctx, cancel := context.WithTimeout(ctx, nodeTimeout)
	defer cancel()
	nets, err := dc.ListNetworks(ctx)
	if err != nil {
		nr.Error = err.Error()
		return nr, nil
	}
	nr.Reachable, nr.Networks = true, len(nets)
	nr.Findings = append(nr.Findings, proxyFindings(srv, nets, exp)...)
	nr.Findings = append(nr.Findings, workspaceFindings(nets, k)...)
	var reserved []ReservedNetwork
	for _, n := range nets {
		if n.Subnet != "" && k.reserved[n.Subnet] {
			reserved = append(reserved, ReservedNetwork{Subnet: n.Subnet, Network: n.Name, NodeID: srv.ID, NodeName: nr.NodeName})
		}
	}
	return nr, reserved
}

func proxyFindings(srv *models.Server, nets []docker.Network, exp Expected) []Finding {
	var out []Finding
	name := exp.Proxy.Name
	var proxy *docker.Network
	for i := range nets {
		switch {
		case nets[i].Name == name:
			proxy = &nets[i]
		case nets[i].Name == exp.LegacyProxy && name != exp.LegacyProxy && (exp.LegacyIsOurs == nil || exp.LegacyIsOurs(nets[i])):
			out = append(out, Finding{Severity: SeverityWarning, Kind: "legacy-proxy", Network: nets[i].Name,
				Message: "The proxy network from before the rename still exists; apps attached to it are unreachable from the gateway until redeployed. Remove it once nothing uses it."})
		}
	}
	runsGateway := srv.IsLocal || srv.Connectivity == models.ConnectivityEdgeGateway
	if proxy == nil {
		if runsGateway {
			out = append(out, Finding{Severity: SeverityWarning, Kind: "proxy-missing", Network: name,
				Message: "This node runs a gateway but has no proxy network; it is created on the next gateway deploy."})
		}
		return out
	}
	if want := exp.Proxy.Subnet; want != "" && proxy.Subnet != want {
		out = append(out, Finding{Severity: SeverityWarning, Kind: "proxy-subnet", Network: name, Expected: want, Actual: proxy.Subnet,
			Message: "The proxy network does not use the configured subnet. Docker cannot change a network's subnet: recreate it to apply."})
	}
	if want := exp.Proxy.EnableIPv6; want != nil && *want != proxy.EnableIPv6 {
		out = append(out, Finding{Severity: SeverityInfo, Kind: "proxy-ipv6", Network: name, Expected: onOff(*want), Actual: onOff(proxy.EnableIPv6),
			Message: "The proxy network's IPv6 setting differs from the platform's; recreate it to apply."})
	}
	return out
}

func workspaceFindings(nets []docker.Network, k *known) []Finding {
	var out []Finding
	for _, n := range nets {
		if !strings.HasPrefix(n.Name, "mb-ws") && !strings.HasPrefix(n.Name, models.StackNetworkPrefix) {
			continue
		}
		model, isWorkspace := k.models[n.Name]
		alloc, allocated := k.ledger[n.Name]
		local := n.Driver == "bridge"
		switch {
		case local && isWorkspace && model.SwarmScoped():
			out = append(out, Finding{Severity: SeverityError, Kind: "shadow-bridge", Network: n.Name, Actual: n.Subnet,
				Message: "A local bridge has the name of a swarm overlay, so containers here may attach to the wrong network. Remove the bridge."})
			continue
		case local && !isWorkspace && !k.stacks[n.Name] && !allocated:
			out = append(out, Finding{Severity: SeverityWarning, Kind: "orphan", Network: n.Name, Actual: n.Subnet,
				Message: "No workspace or stack uses this network any more. Remove it once no container is attached."})
			continue
		}
		switch {
		case allocated && n.Subnet != "" && alloc.Subnet != n.Subnet:
			out = append(out, Finding{Severity: SeverityWarning, Kind: "subnet-mismatch", Network: n.Name, Expected: alloc.Subnet, Actual: n.Subnet,
				Message: "This network's subnet differs from the one the pool pinned it to; recreate it to match the other nodes."})
		case !allocated && n.Subnet != "" && k.pool.IsValid() && !inPrefix(k.pool, n.Subnet):
			out = append(out, Finding{Severity: SeverityInfo, Kind: "outside-pool", Network: n.Name, Actual: n.Subnet,
				Message: "Created on Docker's own address pool rather than Miabi's; recreate it to move it into the pool."})
		}
	}
	return out
}

func (s *Service) checkIngress(ctx context.Context, c *models.Cluster, exp Expected, k *known) ClusterReport {
	cr := ClusterReport{ClusterID: c.ID, ClusterName: c.Label(), Findings: []Finding{}}
	if exp.ManagerOf == nil {
		return cr
	}
	mgr, err := exp.ManagerOf(ctx, c.ID)
	if err != nil {
		cr.Findings = append(cr.Findings, Finding{Severity: SeverityInfo, Kind: "manager-unreachable", Network: exp.IngressName,
			Message: "The swarm manager could not be reached, so the ingress overlay was not checked."})
		return cr
	}
	ctx, cancel := context.WithTimeout(ctx, nodeTimeout)
	defer cancel()
	nets, err := mgr.ListNetworks(ctx)
	if err != nil {
		return cr
	}
	var ingress *docker.Network
	for i := range nets {
		if nets[i].Name == exp.IngressName {
			ingress = &nets[i]
		}
	}
	alloc, allocated := k.ledger[exp.IngressName]
	switch {
	case ingress == nil:
		cr.Findings = append(cr.Findings, Finding{Severity: SeverityInfo, Kind: "ingress-missing", Network: exp.IngressName,
			Message: "The ingress overlay does not exist yet; it is created with the first routed app or gateway attach."})
	case !allocated:
		cr.Findings = append(cr.Findings, Finding{Severity: SeverityWarning, Kind: "ingress-outside-pool", Network: exp.IngressName, Actual: ingress.Subnet,
			Message: "The ingress overlay was not taken from Miabi's pool. Recreate it to move it into the pool."})
	case ingress.Subnet != "" && ingress.Subnet != alloc.Subnet:
		cr.Findings = append(cr.Findings, Finding{Severity: SeverityWarning, Kind: "ingress-subnet", Network: exp.IngressName, Expected: alloc.Subnet, Actual: ingress.Subnet,
			Message: "The ingress overlay's subnet differs from its pool allocation; recreate it to match."})
	}
	return cr
}

func inPrefix(pool netip.Prefix, subnet string) bool {
	p, err := netip.ParsePrefix(subnet)
	return err == nil && pool.Contains(p.Addr())
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
