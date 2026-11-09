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
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestResolveAppResources(t *testing.T) {
	const mb, core = int64(bytesPerMB), int64(nanoPerCore)
	cases := []struct {
		name             string
		limits           Limits
		mem, cpu         int64
		wantMem, wantCPU int64
		wantErr          bool
	}{
		{"no defaults, not required: unlimited stays", Limits{}, 0, 0, 0, 0, false},
		{"defaults fill unset values", Limits{DefaultMemoryMB: 256, DefaultCPUMillicores: 500}, 0, 0, 256 * mb, core / 2, false},
		{"set values win", Limits{DefaultMemoryMB: 256, DefaultCPUMillicores: 500}, 1024 * mb, 2 * core, 1024 * mb, 2 * core, false},
		{"each dimension on its own", Limits{DefaultMemoryMB: 256}, 0, core, 256 * mb, core, false},
		{"required and filled", Limits{RequireResourceLimits: true, DefaultMemoryMB: 128, DefaultCPUMillicores: 250}, 0, 0, 128 * mb, core / 4, false},
		{"required, set by the app", Limits{RequireResourceLimits: true}, 64 * mb, core, 64 * mb, core, false},
		{"required, no default for CPU", Limits{RequireResourceLimits: true, DefaultMemoryMB: 128}, 0, 0, 128 * mb, 0, true},
		{"required, nothing set", Limits{RequireResourceLimits: true}, 0, 0, 0, 0, true},
		{"required, shared CPU needs memory only", Limits{RequireResourceLimits: true, MaxCPUCores: -1}, 64 * mb, 0, 64 * mb, 0, false},
		{"required, shared CPU still needs memory", Limits{RequireResourceLimits: true, MaxCPUCores: -1}, 0, 0, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mem, cpu, err := resolveAppResources(tc.limits, tc.mem, tc.cpu)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrResourceLimitsRequired) {
					t.Errorf("err = %v, want ErrResourceLimitsRequired", err)
				}
				var coded interface{ Code() string }
				if !errors.As(err, &coded) || coded.Code() != "RESOURCE_LIMITS_REQUIRED" {
					t.Errorf("err code = %v, want RESOURCE_LIMITS_REQUIRED", err)
				}
				return
			}
			if mem != tc.wantMem || cpu != tc.wantCPU {
				t.Errorf("got mem=%d cpu=%d, want mem=%d cpu=%d", mem, cpu, tc.wantMem, tc.wantCPU)
			}
		})
	}
}

// Defaults and the requirement come from the plan and can be overridden per workspace.
func TestResourceDefaultsResolution(t *testing.T) {
	five12, yes := 512, true
	base := limitsFromPlan(&models.Plan{DefaultMemoryMB: 128, DefaultCPUMillicores: 250})
	l := applyOverride(base, &models.WorkspaceQuota{DefaultMemoryMB: &five12, RequireResourceLimits: &yes})
	if l.DefaultMemoryMB != 512 || l.DefaultCPUMillicores != 250 || !l.RequireResourceLimits {
		t.Errorf("limits = %+v", l)
	}
	if u := unlimited(); u.RequireResourceLimits || u.DefaultMemoryMB != 0 || u.DefaultCPUMillicores != 0 {
		t.Errorf("unlimited should neither require nor default: %+v", u)
	}
}

// With enforcement off nothing is filled or refused.
func TestResolveAppResourcesDisabled(t *testing.T) {
	mem, cpu, err := NewService(nil, nil, nil, nil, nil, false).ResolveAppResources(1, 0, 0)
	if err != nil || mem != 0 || cpu != 0 {
		t.Errorf("got %d %d %v, want 0 0 nil", mem, cpu, err)
	}
}
