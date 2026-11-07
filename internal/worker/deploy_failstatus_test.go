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

package worker

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestFailedAppStatus(t *testing.T) {
	cases := []struct {
		name       string
		hasCurrent bool
		strategy   models.DeployStrategy
		want       models.AppStatus
	}{
		{"rolling with a live previous release stays running", true, models.DeployRolling, models.AppStatusRunning},
		{"canary with a live previous release stays running", true, models.DeployCanary, models.AppStatusRunning},
		{"empty strategy defaults to keeping the app running", true, "", models.AppStatusRunning},
		{"recreate stopped the old container first — failed", true, models.DeployRecreate, models.AppStatusFailed},
		{"first-ever deploy has nothing running — failed", false, models.DeployRolling, models.AppStatusFailed},
		{"first-ever recreate — failed", false, models.DeployRecreate, models.AppStatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := failedAppStatus(tc.hasCurrent, tc.strategy); got != tc.want {
				t.Errorf("failedAppStatus(%v, %q) = %q, want %q", tc.hasCurrent, tc.strategy, got, tc.want)
			}
		})
	}
}

// Rolling runs both containers at once, which fails on a published host port, so the worker downgrades instead.
func TestEffectiveStrategy(t *testing.T) {
	cases := []struct {
		name      string
		requested models.DeployStrategy
		ports     int
		want      models.DeployStrategy
	}{
		{"rolling with no host ports is left alone", models.DeployRolling, 0, models.DeployRolling},
		{"rolling publishing a host port becomes recreate", models.DeployRolling, 1, models.DeployRecreate},
		{"rolling publishing several still becomes recreate", models.DeployRolling, 3, models.DeployRecreate},
		// Canary publishes no host ports, so it is never downgraded.
		{"canary is never downgraded", models.DeployCanary, 0, models.DeployCanary},
		{"canary is never downgraded even with ports", models.DeployCanary, 2, models.DeployCanary},
		{"recreate already stops the old container first", models.DeployRecreate, 2, models.DeployRecreate},
		{"an unset strategy is not invented", "", 2, ""},
	}
	for _, tc := range cases {
		if got := effectiveStrategy(tc.requested, tc.ports); got != tc.want {
			t.Errorf("%s: effectiveStrategy(%q, %d) = %q, want %q", tc.name, tc.requested, tc.ports, got, tc.want)
		}
	}
}
