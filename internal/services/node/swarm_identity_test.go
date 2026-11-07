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

// Hosts joined by hand stayed unmapped, leaving their replicas' logs and metrics unreachable.
func TestLearnSwarmNodeIDFillsAnUnmappedNode(t *testing.T) {
	repo := newSwarmRepo(t, swarmRow{ID: 1, Name: "dns"})
	s := NewService(repo, nil)

	s.LearnSwarmNodeID(1, "0x3iehtswl9ok63yx1eszfioz")

	if got := swarmIDOf(t, repo, 1); got != "0x3iehtswl9ok63yx1eszfioz" {
		t.Fatalf("swarm node id = %q, want it learned from the agent", got)
	}
}

// The node's own report is more current than ours, so it overwrites a stale id.
func TestLearnSwarmNodeIDOverwritesAStaleID(t *testing.T) {
	repo := newSwarmRepo(t, swarmRow{ID: 1, Name: "dns", SwarmNodeID: "old-swarm-id"})
	s := NewService(repo, nil)

	s.LearnSwarmNodeID(1, "new-swarm-id")

	if got := swarmIDOf(t, repo, 1); got != "new-swarm-id" {
		t.Fatalf("swarm node id = %q, want the agent's current report to win", got)
	}
}

// Empty is not "left the swarm": older agents send no header; the leave paths clear it explicitly.
func TestLearnSwarmNodeIDIgnoresEmpty(t *testing.T) {
	repo := newSwarmRepo(t, swarmRow{ID: 1, Name: "dns", SwarmNodeID: "keep-me"})
	s := NewService(repo, nil)

	s.LearnSwarmNodeID(1, "")
	s.LearnSwarmNodeID(1, "   ")

	if got := swarmIDOf(t, repo, 1); got != "keep-me" {
		t.Fatalf("swarm node id = %q, want it preserved — an absent header is not a departure", got)
	}
}
