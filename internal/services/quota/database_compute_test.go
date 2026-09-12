// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import (
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
