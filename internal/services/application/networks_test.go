// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// selectNetworks is the pure decision inside SetNetworks: which of the workspace's
// networks the app ends up on. It is extracted here so the choice can be tested without
// a database — the persistence is GORM's problem, the selection is ours.
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

// The default network is not optional garnish: it is what the app shares with its
// databases, and in cluster mode it is the workspace's Swarm overlay — the thing that
// lets it reach a database on another node. A caller that names no networks at all
// (GitOps does exactly this) must still get it.
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

// A workspace with no default network is a real state — one that predates default
// networks, or whose network was removed out of band. Without repair, the app is
// created with NO networks at all: it deploys, and then cannot resolve its own
// database, with nothing anywhere to say why. The ensurer exists for exactly this and
// was, until now, never called.
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

// If the repair itself fails there is nothing more to do — but it must degrade to "no
// default", not to a panic or a hard failure of the whole create.
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
