// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// The reported bug: an app configured for canary deployed rolling when the deploy came from CI,
// because the caller passed nothing and nothing consulted the app.
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

// A canary splits traffic between the running release and the new one. On a first deploy there is
// nothing to split, so it has to fall back rather than start a rollout that can never progress.
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
