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

package models

import "testing"

func appWith(def DeployStrategy, running bool) *Application {
	app := &Application{DeployStrategy: def}
	if running {
		id := uint(7)
		app.CurrentReleaseID = &id
	}
	return app
}

// Regression: a canary app deployed rolling from CI because nothing consulted the app default.
func TestUnspecifiedRequestUsesTheAppDefault(t *testing.T) {
	if got := EffectiveDeployStrategy(appWith(DeployCanary, true), ""); got != DeployCanary {
		t.Errorf("got %q, want the app's configured canary", got)
	}
	if got := EffectiveDeployStrategy(appWith(DeployRecreate, true), ""); got != DeployRecreate {
		t.Errorf("got %q, want recreate", got)
	}
}

func TestExplicitRequestWinsOverTheDefault(t *testing.T) {
	if got := EffectiveDeployStrategy(appWith(DeployCanary, true), DeployRecreate); got != DeployRecreate {
		t.Errorf("got %q, want the requested recreate", got)
	}
}

// A first deploy has nothing to split, so canary must fall back rather than start a rollout that never progresses.
func TestCanaryFallsBackWithNoRunningRelease(t *testing.T) {
	if got := EffectiveDeployStrategy(appWith(DeployCanary, false), ""); got != DeployRolling {
		t.Errorf("got %q, want rolling on a first deploy", got)
	}
	if got := EffectiveDeployStrategy(appWith(DeployRolling, false), DeployCanary); got != DeployRolling {
		t.Errorf("got %q, want an explicit canary to fall back too", got)
	}
}

func TestUnknownValuesFallBackToRolling(t *testing.T) {
	if got := EffectiveDeployStrategy(appWith("blue-green", true), ""); got != DeployRolling {
		t.Errorf("an unknown app default gave %q", got)
	}
	if got := EffectiveDeployStrategy(appWith("", true), "nonsense"); got != DeployRolling {
		t.Errorf("an unknown request gave %q", got)
	}
}
