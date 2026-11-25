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

package netfabric

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type removingEngine struct {
	docker.Client
	nets    []docker.Network
	users   map[string][]string
	removed []string
}

func (e *removingEngine) ListNetworks(context.Context) ([]docker.Network, error) { return e.nets, nil }
func (e *removingEngine) ContainersOnNetwork(_ context.Context, name string) ([]string, error) {
	return e.users[name], nil
}
func (e *removingEngine) RemoveNetwork(_ context.Context, name string) error {
	e.removed = append(e.removed, name)
	return nil
}

type releaser struct{ released []string }

func (r *releaser) ReleaseReserved(subnet string) error {
	r.released = append(r.released, subnet)
	return nil
}

var managed = map[string]string{docker.ManagedLabel: "true"}

func TestRemoveOrphansRechecksEveryNetworkBeforeRemoving(t *testing.T) {
	engine := &removingEngine{
		nets: []docker.Network{
			{Name: "mb-ws1-old", Driver: "bridge", Subnet: "10.64.0.0/24", Labels: managed},
			{Name: "mb-ws1-stopped", Driver: "bridge", Subnet: "10.64.1.0/24", Labels: managed},
			{Name: "mb-ws2-live", Driver: "bridge", Subnet: "10.64.2.0/24", Labels: managed},
			{Name: "mb-ws3-foreign", Driver: "bridge", Subnet: "10.64.3.0/24"},
			{Name: "miabi-proxy", Driver: "bridge", Labels: managed},
		},
		users: map[string][]string{"mb-ws1-stopped": {"mb-app-x-1"}},
	}
	rel := &releaser{}
	s := New(fakeNodes{{ID: 3, Name: "safari-01"}}, fakeClients{3: engine}, fakeClusters{},
		fakeLedger{{DockerName: "reserved:10.64.0.0/24", Subnet: "10.64.0.0/24", Kind: models.NetAllocKindReserved}},
		fakeModels{{DockerName: "mb-ws2-live", Driver: "bridge"}}, fakeStacks{},
		func() Expected { return Expected{} })
	s.SetReservedReleaser(rel)

	results, err := s.RemoveOrphans(context.Background(), 3,
		[]string{"mb-ws1-old", "mb-ws1-stopped", "mb-ws2-live", "mb-ws3-foreign", "miabi-proxy", "mb-ws9-gone"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]error{
		"mb-ws1-old":     nil,
		"mb-ws1-stopped": ErrNetworkInUse,
		"mb-ws2-live":    ErrNotOrphan,
		"mb-ws3-foreign": ErrNotMiabis,
		"miabi-proxy":    ErrNotOrphan,
		"mb-ws9-gone":    ErrNetworkAbsent,
	}
	for _, r := range results {
		wantErr := want[r.Network]
		if wantErr == nil && (!r.Removed || r.Error != "") {
			t.Errorf("%s: %+v, want removed", r.Network, r)
		}
		if wantErr != nil && (r.Removed || !containsErr(r.Error, wantErr)) {
			t.Errorf("%s: %+v, want refused with %q", r.Network, r, wantErr)
		}
	}
	if !slices.Equal(engine.removed, []string{"mb-ws1-old"}) {
		t.Errorf("removed %v, want only mb-ws1-old", engine.removed)
	}
	if !slices.Equal(rel.released, []string{"10.64.0.0/24"}) || !results[0].Released {
		t.Errorf("released %v (result %+v), want the reserved subnet of the removed network", rel.released, results[0])
	}
}

func containsErr(msg string, want error) bool {
	return msg != "" && (msg == want.Error() || len(msg) > len(want.Error()) && msg[:len(want.Error())] == want.Error())
}

func TestRemoveOrphansFailsWholeWhenTheNodeIsOffline(t *testing.T) {
	s := New(fakeNodes{}, fakeClients{}, fakeClusters{}, fakeLedger{}, fakeModels{}, fakeStacks{}, func() Expected { return Expected{} })
	if _, err := s.RemoveOrphans(context.Background(), 9, []string{"mb-ws1-x"}); err == nil || errors.Is(err, ErrNotOrphan) {
		t.Errorf("offline node = %v, want the client error", err)
	}
}
