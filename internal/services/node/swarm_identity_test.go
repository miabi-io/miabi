// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package node

import (
	"testing"

	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newSwarmRepo(t *testing.T, seed swarmRow) *repositories.ServerRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&swarmRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	return repositories.NewServerRepository(db)
}

func swarmIDOf(t *testing.T, repo *repositories.ServerRepository, id uint) string {
	t.Helper()
	srv, err := repo.FindByID(id)
	if err != nil {
		t.Fatalf("find node: %v", err)
	}
	return srv.SwarmNodeID
}

// The control plane only ever recorded a swarm node id when Miabi itself ran the
// `swarm join`. A host joined with `docker swarm join`, or one already in a swarm
// when Miabi met it, stayed unmapped forever — and an unmapped node cannot be
// resolved from a service's task, which is exactly what leaves a replica's logs and
// metrics unreachable. The node knows its own id; this is it telling us.
func TestLearnSwarmNodeIDFillsAnUnmappedNode(t *testing.T) {
	repo := newSwarmRepo(t, swarmRow{ID: 1, Name: "dns"})
	s := NewService(repo, nil)

	s.LearnSwarmNodeID(1, "0x3iehtswl9ok63yx1eszfioz")

	if got := swarmIDOf(t, repo, 1); got != "0x3iehtswl9ok63yx1eszfioz" {
		t.Fatalf("swarm node id = %q, want it learned from the agent", got)
	}
}

// A node can leave one swarm and join another. Its own report is always more current
// than ours, so unlike LearnEndpoint (which only fills blanks) this overwrites.
func TestLearnSwarmNodeIDOverwritesAStaleID(t *testing.T) {
	repo := newSwarmRepo(t, swarmRow{ID: 1, Name: "dns", SwarmNodeID: "old-swarm-id"})
	s := NewService(repo, nil)

	s.LearnSwarmNodeID(1, "new-swarm-id")

	if got := swarmIDOf(t, repo, 1); got != "new-swarm-id" {
		t.Fatalf("swarm node id = %q, want the agent's current report to win", got)
	}
}

// An empty value must NOT be read as "this node left the swarm": an older agent
// sends no header at all, and wiping a good id because of it would re-break exactly
// the lookup this feature exists to fix. The leave paths clear it explicitly.
func TestLearnSwarmNodeIDIgnoresEmpty(t *testing.T) {
	repo := newSwarmRepo(t, swarmRow{ID: 1, Name: "dns", SwarmNodeID: "keep-me"})
	s := NewService(repo, nil)

	s.LearnSwarmNodeID(1, "")
	s.LearnSwarmNodeID(1, "   ")

	if got := swarmIDOf(t, repo, 1); got != "keep-me" {
		t.Fatalf("swarm node id = %q, want it preserved — an absent header is not a departure", got)
	}
}
