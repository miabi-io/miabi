// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package netalloc hands out non-overlapping subnets, carved from a single platform pool, for every Docker
// network Miabi creates. It works around Docker's tiny built-in default-address-pools by allocating an
// explicit /N per network. A DB ledger is the source of truth, so allocations survive restarts.
package netalloc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/metrics"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// ErrPoolExhausted is returned when every subnet in the pool is allocated or
// reserved. The operator must enlarge MIABI_NETWORK_POOL_CIDR.
var ErrPoolExhausted = errors.New("network subnet pool exhausted")

// NetworkProvisioner is the subset of docker.Client the allocator drives — kept
// small so both the full client and narrower service-level interfaces (e.g. the
// stack service's NetworkManager) satisfy it.
type NetworkProvisioner interface {
	EnsureNetworkSpec(ctx context.Context, spec docker.NetworkSpec) (string, error)
	ListNetworks(ctx context.Context) ([]docker.Network, error)
}

// Service allocates subnets from the configured pool.
type Service struct {
	repo   *repositories.NetworkAllocationRepository
	base   uint32 // pool network address, as a big-endian uint32
	total  uint32 // number of subnets of `prefix` size that fit in the pool
	step   uint32 // addresses per subnet (2^(32-prefix))
	prefix int    // per-network subnet prefix length
	mu     sync.Mutex
}

// NewService builds the allocator for an IPv4 pool CIDR and a per-network subnet prefix. Returns an error
// on an invalid or IPv6 pool, or a prefix that doesn't fit inside it — config.validate catches these first
// at boot, so this is defence in depth.
func NewService(repo *repositories.NetworkAllocationRepository, poolCIDR string, subnetPrefix int) (*Service, error) {
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(poolCIDR))
	if err != nil {
		return nil, fmt.Errorf("parse network pool %q: %w", poolCIDR, err)
	}
	v4 := ip.To4()
	if v4 == nil {
		return nil, fmt.Errorf("network pool %q must be IPv4", poolCIDR)
	}
	poolPrefix, bits := ipnet.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("network pool %q must be IPv4", poolCIDR)
	}
	if subnetPrefix <= poolPrefix || subnetPrefix > 30 {
		return nil, fmt.Errorf("subnet prefix /%d does not fit in pool /%d", subnetPrefix, poolPrefix)
	}
	base := binary.BigEndian.Uint32(ipnet.IP.To4())
	return &Service{
		repo:   repo,
		base:   base,
		total:  1 << uint(subnetPrefix-poolPrefix),
		step:   1 << uint(32-subnetPrefix),
		prefix: subnetPrefix,
	}, nil
}

// subnetAt returns the CIDR and gateway (first usable host) of the i-th subnet.
func (s *Service) subnetAt(i uint32) (subnet, gateway string) {
	netAddr := s.base + i*s.step
	subnet = fmt.Sprintf("%s/%d", u32ToIP(netAddr), s.prefix)
	gateway = u32ToIP(netAddr + 1).String()
	return subnet, gateway
}

func u32ToIP(v uint32) net.IP {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return net.IP(b)
}

// Capacity is the total number of subnets the pool can hold.
func (s *Service) Capacity() int { return int(s.total) }

// Stats reports pool utilization (used includes reserved rows).
func (s *Service) Stats() (used int, total int) {
	n, _ := s.repo.Count()
	return int(n), int(s.total)
}

// PublishStats updates the Prometheus pool-utilization gauges. Called after every
// mutation so /metrics reflects the current usage without a background loop.
func (s *Service) PublishStats() {
	used, total := s.Stats()
	metrics.SetSubnetPoolUsage(used, total)
}

// Allocate reserves a subnet for dockerName idempotently: an existing allocation for the same name is
// returned unchanged, otherwise the lowest-index free subnet is reserved and persisted. Callers that
// create the Docker network should prefer EnsureManaged, which also handles host-level overlaps.
func (s *Service) Allocate(dockerName string, nodeID uint, kind string) (*models.NetworkAllocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.PublishStats()
	return s.allocateLocked(dockerName, nodeID, kind)
}

func (s *Service) allocateLocked(dockerName string, nodeID uint, kind string) (*models.NetworkAllocation, error) {
	if a, err := s.repo.FindByDockerName(dockerName); err == nil {
		return a, nil
	}
	used, err := s.usedSet()
	if err != nil {
		return nil, err
	}
	for i := uint32(0); i < s.total; i++ {
		subnet, gateway := s.subnetAt(i)
		if used[subnet] {
			continue
		}
		a := &models.NetworkAllocation{DockerName: dockerName, Subnet: subnet, Gateway: gateway, NodeID: nodeID, Kind: kind}
		if err := s.repo.Create(a); err != nil {
			// A concurrent allocator took this subnet (unique constraint) — skip it.
			used[subnet] = true
			continue
		}
		return a, nil
	}
	return nil, ErrPoolExhausted
}

func (s *Service) usedSet() (map[string]bool, error) {
	subnets, err := s.repo.AllSubnets()
	if err != nil {
		return nil, err
	}
	used := make(map[string]bool, len(subnets))
	for _, sn := range subnets {
		used[sn] = true
	}
	return used, nil
}

// Release frees the allocation for a removed network so its subnet returns to the
// pool. Idempotent.
func (s *Service) Release(dockerName string) error {
	err := s.repo.DeleteByDockerName(dockerName)
	s.PublishStats()
	return err
}

func (s *Service) EnsureManaged(ctx context.Context, dc NetworkProvisioner, spec docker.NetworkSpec, nodeID uint, kind string) (id, subnet string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.PublishStats()

	// Reuse an existing allocation for this network name (e.g. remote-node recreate,
	// or a redeploy) so the subnet is stable across nodes and restarts.
	if a, ferr := s.repo.FindByDockerName(spec.Name); ferr == nil {
		spec.Subnet, spec.Gateway = a.Subnet, a.Gateway
		nid, cerr := dc.EnsureNetworkSpec(ctx, spec)
		return nid, a.Subnet, cerr
	}

	const maxAttempts = 8 // bounded retries past host-level overlaps
	for attempt := 0; attempt < maxAttempts; attempt++ {
		a, aerr := s.allocateLocked(spec.Name, nodeID, kind)
		if aerr != nil {
			return "", "", aerr
		}
		spec.Subnet, spec.Gateway = a.Subnet, a.Gateway
		nid, cerr := dc.EnsureNetworkSpec(ctx, spec)
		if cerr == nil {
			return nid, a.Subnet, nil
		}
		if isSubnetOverlap(cerr) {
			// Not ours but occupies the pool: convert the allocation into a permanent
			// reservation and try the next subnet.
			_ = s.repo.DeleteByDockerName(spec.Name)
			_ = s.repo.Create(&models.NetworkAllocation{
				DockerName: reservedName(a.Subnet), Subnet: a.Subnet, Gateway: a.Gateway,
				NodeID: nodeID, Kind: models.NetAllocKindReserved,
			})
			logger.Warn("network subnet overlaps an existing network; reserving and retrying", "subnet", a.Subnet, "network", spec.Name)
			continue
		}
		// A real failure: release our reservation and surface the error.
		_ = s.repo.DeleteByDockerName(spec.Name)
		return "", "", cerr
	}
	return "", "", fmt.Errorf("could not allocate a non-overlapping subnet for %q after %d attempts", spec.Name, maxAttempts)
}

// ImportExisting reserves any pool subnet already in use by a Docker network on
// the node, so the allocator never hands out an overlapping one. Best-effort and
// idempotent — called at boot. Networks Miabi already tracks are skipped.
func (s *Service) ImportExisting(ctx context.Context, dc NetworkProvisioner, nodeID uint) error {
	nets, err := dc.ListNetworks(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reserved := 0
	for _, n := range nets {
		if n.Subnet == "" || !s.inPool(n.Subnet) {
			continue
		}
		if exists, _ := s.repo.ExistsBySubnet(n.Subnet); exists {
			continue
		}
		if err := s.repo.Create(&models.NetworkAllocation{
			DockerName: reservedName(n.Subnet), Subnet: n.Subnet, Gateway: "",
			NodeID: nodeID, Kind: models.NetAllocKindReserved,
		}); err == nil {
			reserved++
		}
	}
	if reserved > 0 {
		logger.Info("reserved pre-existing network subnets in the pool", "count", reserved, "node", nodeID)
	}
	s.PublishStats()
	return nil
}

// inPool reports whether a subnet's network address falls within the pool and
// aligns to a subnet boundary (so it maps to one of our indices).
func (s *Service) inPool(subnet string) bool {
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(subnet))
	if err != nil || ipnet.IP.To4() == nil {
		return false
	}
	addr := binary.BigEndian.Uint32(ipnet.IP.To4())
	if addr < s.base {
		return false
	}
	end := s.base + s.total*s.step // one past the pool
	return addr < end
}

func reservedName(subnet string) string { return "reserved:" + subnet }

// isSubnetOverlap matches Docker's "Pool overlaps with other one" / "overlaps"
// errors so the allocator can skip a subnet that collides with something outside
// its ledger.
func isSubnetOverlap(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "overlap")
}
