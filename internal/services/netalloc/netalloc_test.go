// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package netalloc

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newService(t *testing.T, pool string, prefix int) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.NetworkAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc, err := NewService(repositories.NewNetworkAllocationRepository(db), pool, prefix)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func TestNewServiceValidation(t *testing.T) {
	repo := (*repositories.NetworkAllocationRepository)(nil)
	if _, err := NewService(repo, "not-a-cidr", 24); err == nil {
		t.Error("expected error for invalid CIDR")
	}
	if _, err := NewService(repo, "10.42.0.0/16", 16); err == nil {
		t.Error("expected error when subnet prefix does not fit in pool")
	}
	if _, err := NewService(repo, "fd00::/48", 64); err == nil {
		t.Error("expected error for IPv6 pool")
	}
}

func TestSubnetMathAndCapacity(t *testing.T) {
	s := newService(t, "10.42.0.0/16", 24)
	if got := s.Capacity(); got != 256 {
		t.Fatalf("capacity = %d, want 256", got)
	}
	subnet, gw := s.subnetAt(0)
	if subnet != "10.42.0.0/24" || gw != "10.42.0.1" {
		t.Errorf("subnetAt(0) = %s / %s, want 10.42.0.0/24 / 10.42.0.1", subnet, gw)
	}
	subnet, gw = s.subnetAt(3)
	if subnet != "10.42.3.0/24" || gw != "10.42.3.1" {
		t.Errorf("subnetAt(3) = %s / %s, want 10.42.3.0/24 / 10.42.3.1", subnet, gw)
	}
}

func TestAllocateIsIdempotentAndSequential(t *testing.T) {
	s := newService(t, "10.42.0.0/16", 24)
	a1, err := s.Allocate("mb-a", 0, models.NetAllocKindStack)
	if err != nil {
		t.Fatalf("allocate a: %v", err)
	}
	if a1.Subnet != "10.42.0.0/24" {
		t.Errorf("first subnet = %s, want 10.42.0.0/24", a1.Subnet)
	}
	// Re-allocating the same name returns the same subnet.
	again, err := s.Allocate("mb-a", 0, models.NetAllocKindStack)
	if err != nil || again.Subnet != a1.Subnet {
		t.Errorf("re-allocate = %v (%v), want stable %s", again, err, a1.Subnet)
	}
	// A different name gets the next free subnet.
	a2, err := s.Allocate("mb-b", 0, models.NetAllocKindStack)
	if err != nil || a2.Subnet != "10.42.1.0/24" {
		t.Errorf("second subnet = %v (%v), want 10.42.1.0/24", a2, err)
	}
}

func TestReleaseReturnsSubnetToPool(t *testing.T) {
	s := newService(t, "10.42.0.0/16", 24)
	a, _ := s.Allocate("mb-a", 0, models.NetAllocKindStack)
	if err := s.Release("mb-a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	reused, _ := s.Allocate("mb-c", 0, models.NetAllocKindStack)
	if reused.Subnet != a.Subnet {
		t.Errorf("freed subnet not reused: got %s, want %s", reused.Subnet, a.Subnet)
	}
}

func TestPoolExhaustion(t *testing.T) {
	// A /23 pool split into /24 yields exactly 2 subnets.
	s := newService(t, "10.42.0.0/23", 24)
	if s.Capacity() != 2 {
		t.Fatalf("capacity = %d, want 2", s.Capacity())
	}
	if _, err := s.Allocate("n1", 0, "x"); err != nil {
		t.Fatalf("n1: %v", err)
	}
	if _, err := s.Allocate("n2", 0, "x"); err != nil {
		t.Fatalf("n2: %v", err)
	}
	if _, err := s.Allocate("n3", 0, "x"); !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("n3 err = %v, want ErrPoolExhausted", err)
	}
}

// fakeDocker records EnsureNetworkSpec calls and can force an overlap error on a
// specific subnet, to exercise the reserve-and-retry path.
type fakeDocker struct {
	docker.Client
	overlapSubnet string
	created       []string // subnets successfully created
	existing      []docker.Network
}

func (f *fakeDocker) EnsureNetworkSpec(_ context.Context, spec docker.NetworkSpec) (string, error) {
	if spec.Subnet == f.overlapSubnet {
		return "", errors.New("Pool overlaps with other one on this address space")
	}
	f.created = append(f.created, spec.Subnet)
	return "id-" + spec.Subnet, nil
}
func (f *fakeDocker) ListNetworks(context.Context) ([]docker.Network, error) {
	return f.existing, nil
}

func TestEnsureManagedSkipsOverlap(t *testing.T) {
	s := newService(t, "10.42.0.0/16", 24)
	dc := &fakeDocker{overlapSubnet: "10.42.0.0/24"} // first subnet collides
	id, subnet, err := s.EnsureManaged(context.Background(), dc, docker.NetworkSpec{Name: "mb-stack"}, 0, models.NetAllocKindStack)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if subnet != "10.42.1.0/24" {
		t.Errorf("subnet = %s, want 10.42.1.0/24 (skipped the overlapping first)", subnet)
	}
	if id != "id-10.42.1.0/24" {
		t.Errorf("id = %s", id)
	}
	// The overlapping subnet is now reserved, so the next allocation skips it too.
	next, _ := s.Allocate("mb-other", 0, models.NetAllocKindStack)
	if next.Subnet == "10.42.0.0/24" {
		t.Error("overlapping subnet was handed out again")
	}
}

func TestEnsureManagedReusesExisting(t *testing.T) {
	s := newService(t, "10.42.0.0/16", 24)
	dc := &fakeDocker{}
	_, first, _ := s.EnsureManaged(context.Background(), dc, docker.NetworkSpec{Name: "mb-x"}, 0, models.NetAllocKindStack)
	_, again, _ := s.EnsureManaged(context.Background(), dc, docker.NetworkSpec{Name: "mb-x"}, 0, models.NetAllocKindStack)
	if first != again {
		t.Errorf("subnet not stable across calls: %s vs %s", first, again)
	}
}

func TestImportExistingReservesOverlap(t *testing.T) {
	s := newService(t, "10.42.0.0/16", 24)
	dc := &fakeDocker{existing: []docker.Network{
		{Name: "prev", Subnet: "10.42.5.0/24"},  // in pool → reserved
		{Name: "lan", Subnet: "192.168.1.0/24"}, // outside pool → ignored
		{Name: "nosubnet"},                      // no subnet → ignored
	}}
	if err := s.ImportExisting(context.Background(), dc, 0); err != nil {
		t.Fatalf("import: %v", err)
	}
	// The reserved subnet must not be handed out.
	for i := 0; i < 6; i++ {
		a, err := s.Allocate("n"+string(rune('a'+i)), 0, "x")
		if err != nil {
			t.Fatalf("allocate: %v", err)
		}
		if a.Subnet == "10.42.5.0/24" {
			t.Fatal("reserved (pre-existing) subnet was handed out")
		}
	}
}
