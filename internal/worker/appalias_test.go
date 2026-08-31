// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

func appWithNetworks(name string, nets ...string) *models.Application {
	app := &models.Application{Name: name}
	for _, n := range nets {
		app.Networks = append(app.Networks, models.Network{DockerName: n})
	}
	return app
}

// The app answers to its own name on its workspace networks, which is what makes
// {{ .applications.<name>.host }} an address rather than a guess.
func TestAppRegistersItsNameOnWorkspaceNetworks(t *testing.T) {
	got := nameAliasesByNetwork(appWithNetworks("api", "mb-ws3-a1b2c3"), false)
	if !slices.Contains(got["mb-ws3-a1b2c3"], "api") {
		t.Errorf("aliases = %v, want the app name on its workspace network", got)
	}
}

// The guard that matters: the shared proxy network spans every workspace on the host, so a bare
// name there would let one tenant's "api" answer for another's. It must never be registered by
// this path — only by the per-network map, for networks the app actually owns.
func TestAppNameIsNeverRegisteredOnTheProxyNetwork(t *testing.T) {
	got := nameAliasesByNetwork(appWithNetworks("api", "mb-ws3-a1b2c3"), false)
	if _, ok := got[node.AppNetwork]; ok {
		t.Fatalf("the shared proxy network got a bare-name alias: %v", got)
	}
	for net := range got {
		if net == node.AppNetwork {
			t.Errorf("proxy network present in %v", got)
		}
	}
}

// A stacked app keeps answering to its service name on the stack network — the behaviour siblings
// in a stack already rely on.
func TestStackAliasStillRegistered(t *testing.T) {
	app := appWithNetworks("api", "mb-ws3-a1b2c3")
	app.Stack = &models.Stack{DockerNetwork: "mb-stack-7"}
	got := nameAliasesByNetwork(app, false)
	if !slices.Contains(got["mb-stack-7"], "api") {
		t.Errorf("aliases = %v, want the service name on the stack network", got)
	}
}

// Docker refuses a duplicate alias on one attachment, so a stack network that is also listed as a
// workspace network must yield the name once.
func TestAliasIsNotRegisteredTwiceOnOneNetwork(t *testing.T) {
	app := appWithNetworks("api", "mb-stack-7")
	app.Stack = &models.Stack{DockerNetwork: "mb-stack-7"}
	if got := nameAliasesByNetwork(app, false)["mb-stack-7"]; len(got) != 1 {
		t.Errorf("aliases = %v, want the name once", got)
	}
}

// A canary runs beside the stable release. Taking the workspace name would send a sibling's traffic
// to the release under test by DNS round-robin, which is the opposite of a controlled rollout.
func TestCanaryDoesNotTakeTheWorkspaceName(t *testing.T) {
	got := nameAliasesByNetwork(appWithNetworks("api", "mb-ws3-a1b2c3"), true)
	if slices.Contains(got["mb-ws3-a1b2c3"], "api") {
		t.Errorf("a canary claimed the workspace name: %v", got)
	}
}

// An app with no networks yet registers nothing rather than producing an empty-keyed entry Docker
// would reject.
func TestNoNetworksYieldsNoAliases(t *testing.T) {
	app := appWithNetworks("api")
	app.Networks = append(app.Networks, models.Network{DockerName: ""})
	if got := nameAliasesByNetwork(app, false); len(got) != 0 {
		t.Errorf("aliases = %v, want none", got)
	}
}
