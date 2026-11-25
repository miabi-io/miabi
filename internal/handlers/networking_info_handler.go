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

package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/netfabric"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Operator-configured through env or the install manifest, so this reports rather than edits.
type NetworkingInfo struct {
	// On an engine below 26 with no ULA prefix Miabi refuses IPv6, so asked-for and active can differ.
	IPv6Enabled   bool   `json:"ipv6_enabled"`
	IPv6Active    bool   `json:"ipv6_active"`
	IPv6ULAPrefix string `json:"ipv6_ula_prefix,omitempty"`
	IPv6Reason    string `json:"ipv6_reason,omitempty"`

	PoolCIDR     string `json:"pool_cidr,omitempty"`
	SubnetPrefix int    `json:"subnet_prefix,omitempty"`
	// PoolEnabled is false when the allocator could not start, leaving Docker's own pool in use.
	PoolEnabled bool `json:"pool_enabled"`
	PoolUsed    int  `json:"pool_used"`
	PoolTotal   int  `json:"pool_total"`

	ProxyNetwork string `json:"proxy_network,omitempty"`
	ProxySubnet  string `json:"proxy_subnet,omitempty"`
	// ProxySubnetSource is configured (MIABI_PROXY_SUBNET), control-plane (copied from its network) or default.
	ProxySubnetSource string `json:"proxy_subnet_source,omitempty"`
}

// PoolStats reports the allocator's usage. Satisfied by *netalloc.Service; pass a nil interface when it is off.
type PoolStats interface {
	Stats() (used int, total int)
}

type NetworkingHandler struct {
	audit       *audit.Logger
	fabric      *netfabric.Service
	info        NetworkingInfo
	pool        PoolStats
	allocations *repositories.NetworkAllocationRepository
}

// IPv6 activity depends on the engine, so it is resolved at boot and passed in.
func NewNetworkingHandler(info NetworkingInfo, pool PoolStats, allocations *repositories.NetworkAllocationRepository) *NetworkingHandler {
	if info.IPv6Enabled && !info.IPv6Active && strings.TrimSpace(info.IPv6Reason) == "" {
		info.IPv6Reason = "this Docker Engine cannot assign an IPv6 range itself; upgrade to Engine 26+ or set MIABI_NETWORK_IPV6_ULA_PREFIX"
	}
	return &NetworkingHandler{info: info, pool: pool, allocations: allocations}
}

func (h *NetworkingHandler) Info(c *okapi.Context) error {
	out := h.info
	if h.pool != nil {
		out.PoolEnabled = true
		out.PoolUsed, out.PoolTotal = h.pool.Stats()
	}
	out.ProxySubnet, out.ProxySubnetSource = node.AppNetworkSource()
	return ok(c, out)
}

type NetworkAllocationsResponse struct {
	Allocations []repositories.AllocationDetail `json:"allocations"`
}

// Allocations is the subnet ledger, ordered by address rather than by the string form of the CIDR.
func (h *NetworkingHandler) Allocations(c *okapi.Context) error {
	rows, err := h.allocations.ListDetailed()
	if err != nil {
		return c.AbortInternalServerError("failed to list network allocations", err)
	}
	sort.SliceStable(rows, func(i, j int) bool { return subnetLess(rows[i].Subnet, rows[j].Subnet) })
	return ok(c, NetworkAllocationsResponse{Allocations: rows})
}

func subnetLess(a, b string) bool {
	pa, ea := netip.ParsePrefix(a)
	pb, eb := netip.ParsePrefix(b)
	if ea != nil || eb != nil {
		return a < b
	}
	return pa.Addr().Less(pb.Addr())
}

func (h *NetworkingHandler) SetFabric(f *netfabric.Service, auditLog *audit.Logger) {
	h.fabric, h.audit = f, auditLog
}

type RemoveOrphanNetworksRequest struct {
	Body struct {
		NodeID uint `json:"node_id" required:"true"`
		// Networks are Docker network names the check reported as orphans; each is checked again before removal.
		Networks []string `json:"networks" required:"true" minItems:"1" maxItems:"200"`
	} `json:"body"`
}

type RemoveOrphanNetworksResponse struct {
	Results []netfabric.RemoveResult `json:"results"`
}

func (h *NetworkingHandler) RemoveOrphans(c *okapi.Context, req *RemoveOrphanNetworksRequest) error {
	if h.fabric == nil {
		return c.AbortWithError(http.StatusServiceUnavailable, errors.New("the network check is not available in this process"))
	}
	results, err := h.fabric.RemoveOrphans(c.Request().Context(), req.Body.NodeID, req.Body.Networks)
	if err != nil {
		return c.AbortWithError(http.StatusBadGateway, fmt.Errorf("the node's networks could not be read: %w", err))
	}
	actor := middlewares.UserID(c)
	for _, r := range results {
		if r.Removed && h.audit != nil {
			h.audit.Record(audit.Entry{ActorID: &actor, Action: "networking.orphan_remove", TargetType: "network",
				TargetID: fmt.Sprintf("%d/%s", req.Body.NodeID, r.Network), IP: c.RealIP()})
		}
	}
	return ok(c, RemoveOrphanNetworksResponse{Results: results})
}

// Fabric asks every node's engine for its networks, so it runs on demand rather than with the page.
func (h *NetworkingHandler) Fabric(c *okapi.Context) error {
	if h.fabric == nil {
		return c.AbortWithError(http.StatusServiceUnavailable, errors.New("the network check is not available in this process"))
	}
	rep, err := h.fabric.Check(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to check the nodes' networks", err)
	}
	return ok(c, rep)
}
