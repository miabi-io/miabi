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

package quota

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// The database budget resolves through the plan and its override on its own, never borrowing the apps'.
func TestDatabaseComputeWiring(t *testing.T) {
	l := limitsFromPlan(&models.Plan{MaxDatabaseCPUCores: 4, MaxDatabaseMemoryMB: 8192, MaxCPUCores: 2, MaxMemoryMB: 1024})
	if l.MaxDatabaseCPUCores != 4 || l.MaxDatabaseMemoryMB != 8192 || l.MaxCPUCores != 2 || l.MaxMemoryMB != 1024 {
		t.Errorf("limits = %+v", l)
	}
	none := 0
	got := applyOverride(l, &models.WorkspaceQuota{MaxDatabaseMemoryMB: &none})
	if got.MaxDatabaseMemoryMB != 0 || got.MaxDatabaseCPUCores != 4 {
		t.Errorf("override: cpu = %d, memory = %d, want 4 and 0", got.MaxDatabaseCPUCores, got.MaxDatabaseMemoryMB)
	}
	if u := unlimited(); u.MaxDatabaseCPUCores != models.Unlimited || u.MaxDatabaseMemoryMB != models.Unlimited {
		t.Errorf("unlimited = %d / %d", u.MaxDatabaseCPUCores, u.MaxDatabaseMemoryMB)
	}
}

// Overcommit stretches the summed CPU limits a cap admits, never an unlimited one, and never below the plan.
func TestCheckCPUOvercommit(t *testing.T) {
	const core = int64(nanoPerCore)
	plain := Limits{CPUOvercommit: 1}
	if err := checkCPU(plain, 2, 2*core, "workspace"); err != nil {
		t.Errorf("at the cap: %v", err)
	}
	if err := checkCPU(plain, 2, 3*core, "workspace"); err == nil || !strings.Contains(err.Error(), "2-core limit") {
		t.Errorf("past the cap without overcommit: %v", err)
	}
	over := Limits{CPUOvercommit: 2.5}
	if err := checkCPU(over, 2, 5*core, "workspace"); err != nil {
		t.Errorf("within the allowance: %v", err)
	}
	if err := checkCPU(over, 2, 5*core+1, "database"); err == nil || !strings.Contains(err.Error(), "5 cores allowed for a 2-core plan") {
		t.Errorf("past the allowance: %v", err)
	}
	if err := checkCPU(over, -1, 100*core, "workspace"); err != nil {
		t.Errorf("unlimited: %v", err)
	}
	if got := (Limits{CPUOvercommit: 0.5}).CPUAllowance(2); got != 2 {
		t.Errorf("a ratio below 1 shrank the cap to %v", got)
	}
}
