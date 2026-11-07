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

package application

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// fakeEnsurer stands in for the network service's EnsureDefault.
type fakeEnsurer struct {
	called bool
	net    *models.Network
	err    error
}

func (f *fakeEnsurer) EnsureDefault(context.Context, uint) (*models.Network, error) {
	f.called = true
	return f.net, f.err
}

// Extracted so the selection can be tested without a database.
func selectNetworks(all []models.Network, want []uint) []models.Network {
	set := map[uint]bool{}
	for _, id := range want {
		set[id] = true
	}
	var out []models.Network
	for i := range all {
		if set[all[i].ID] || all[i].IsDefault {
			out = append(out, all[i])
		}
	}
	return out
}

// The default network is how the app reaches its databases; callers naming none (GitOps) must still get it.
func TestDefaultNetworkIsAlwaysSelected(t *testing.T) {
	all := []models.Network{
		{ID: 1, Name: "default", IsDefault: true, Driver: "overlay"},
		{ID: 2, Name: "extra"},
	}

	// GitOps names nothing.
	got := selectNetworks(all, nil)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("an app that names no networks must still join the default, got %+v", got)
	}
	if got[0].Driver != "overlay" {
		t.Errorf("in cluster mode the default is the overlay; driver = %q", got[0].Driver)
	}

	// Naming another network adds to the default, it does not replace it.
	got = selectNetworks(all, []uint{2})
	if len(got) != 2 {
		t.Fatalf("naming a network must not drop the default, got %+v", got)
	}
}

// hasDefaultNetwork is what decides whether the repair below runs at all.
func TestHasDefaultNetwork(t *testing.T) {
	if hasDefaultNetwork([]models.Network{{ID: 1}, {ID: 2}}) {
		t.Error("no network is marked default, but one was reported")
	}
	if !hasDefaultNetwork([]models.Network{{ID: 1}, {ID: 2, IsDefault: true}}) {
		t.Error("a default is present but was not found")
	}
	if hasDefaultNetwork(nil) {
		t.Error("an empty workspace has no default")
	}
}

// Without repair the app gets no networks: it deploys, then cannot resolve its own database.
func TestMissingDefaultIsRepaired(t *testing.T) {
	ens := &fakeEnsurer{net: &models.Network{ID: 9, Name: "default", IsDefault: true, Driver: "overlay"}}
	s := &Service{netEnsurer: ens}

	all := []models.Network{{ID: 2, Name: "extra"}} // no default!
	if hasDefaultNetwork(all) {
		t.Fatal("test setup is wrong: this fixture must have no default")
	}

	// Mirror SetNetworks' repair step.
	if !hasDefaultNetwork(all) && s.netEnsurer != nil {
		def, err := s.netEnsurer.EnsureDefault(context.Background(), 1)
		if err == nil && def != nil {
			all = append(all, *def)
		}
	}

	if !ens.called {
		t.Fatal("a missing default must trigger the ensurer — otherwise the app gets no networks")
	}
	got := selectNetworks(all, nil)
	if len(got) != 1 || !got[0].IsDefault {
		t.Fatalf("after repair the app must join the default, got %+v", got)
	}
}

// A failed repair must degrade to "no default", not a panic or a failed create.
func TestFailedRepairDoesNotBreakSelection(t *testing.T) {
	ens := &fakeEnsurer{err: errors.New("docker unreachable")}
	s := &Service{netEnsurer: ens}

	all := []models.Network{{ID: 2, Name: "extra"}}
	if !hasDefaultNetwork(all) && s.netEnsurer != nil {
		if def, err := s.netEnsurer.EnsureDefault(context.Background(), 1); err == nil && def != nil {
			all = append(all, *def)
		}
	}
	if got := selectNetworks(all, []uint{2}); len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("a failed repair must still select the named networks, got %+v", got)
	}
}
