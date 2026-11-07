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

func TestAppRegistersItsNameOnWorkspaceNetworks(t *testing.T) {
	got := nameAliasesByNetwork(appWithNetworks("api", "mb-ws3-a1b2c3"), false)
	if !slices.Contains(got["mb-ws3-a1b2c3"], "api") {
		t.Errorf("aliases = %v, want the app name on its workspace network", got)
	}
}

// The shared proxy network spans every workspace, so a bare name there would let one tenant answer for another's.
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

func TestStackAliasStillRegistered(t *testing.T) {
	app := appWithNetworks("api", "mb-ws3-a1b2c3")
	app.Stack = &models.Stack{DockerNetwork: "mb-stack-7"}
	got := nameAliasesByNetwork(app, false)
	if !slices.Contains(got["mb-stack-7"], "api") {
		t.Errorf("aliases = %v, want the service name on the stack network", got)
	}
}

// Docker refuses a duplicate alias on one attachment.
func TestAliasIsNotRegisteredTwiceOnOneNetwork(t *testing.T) {
	app := appWithNetworks("api", "mb-stack-7")
	app.Stack = &models.Stack{DockerNetwork: "mb-stack-7"}
	if got := nameAliasesByNetwork(app, false)["mb-stack-7"]; len(got) != 1 {
		t.Errorf("aliases = %v, want the name once", got)
	}
}

// A canary taking the workspace name would get a sibling's traffic by DNS round-robin.
func TestCanaryDoesNotTakeTheWorkspaceName(t *testing.T) {
	got := nameAliasesByNetwork(appWithNetworks("api", "mb-ws3-a1b2c3"), true)
	if slices.Contains(got["mb-ws3-a1b2c3"], "api") {
		t.Errorf("a canary claimed the workspace name: %v", got)
	}
}

// Docker would reject an empty-keyed alias entry.
func TestNoNetworksYieldsNoAliases(t *testing.T) {
	app := appWithNetworks("api")
	app.Networks = append(app.Networks, models.Network{DockerName: ""})
	if got := nameAliasesByNetwork(app, false); len(got) != 0 {
		t.Errorf("aliases = %v, want none", got)
	}
}
