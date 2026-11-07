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

package network

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// fakeCluster stubs swarm detection.
type fakeCluster struct{ on bool }

func (f fakeCluster) IsSwarm(uint) bool { return f.on }

// Recorded so tests can assert ordering (connect must precede disconnect) as well as content.
type call struct {
	op        string // "connect" | "disconnect"
	network   string
	container string
	aliases   []string
}

type fakeDocker struct {
	docker.Client
	containers []docker.Container
	inspect    map[string]docker.Container
	calls      []call
	removed    []string
	connectErr error
}

func (f *fakeDocker) ListContainers(context.Context, bool) ([]docker.Container, error) {
	return f.containers, nil
}

func (f *fakeDocker) InspectContainer(_ context.Context, id string) (docker.Container, error) {
	c, ok := f.inspect[id]
	if !ok {
		return docker.Container{}, errors.New("no such container")
	}
	return c, nil
}

func (f *fakeDocker) NetworkConnect(_ context.Context, name, id string, aliases []string) error {
	if f.connectErr != nil {
		return f.connectErr
	}
	f.calls = append(f.calls, call{op: "connect", network: name, container: id, aliases: aliases})
	return nil
}

func (f *fakeDocker) NetworkDisconnect(_ context.Context, name, id string, _ bool) error {
	f.calls = append(f.calls, call{op: "disconnect", network: name, container: id})
	return nil
}

func (f *fakeDocker) RemoveNetwork(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

// Pins that aliases survive the move and the container is attached to the overlay before leaving the bridge.
func TestMoveContainersCarriesAliasesAndConnectsFirst(t *testing.T) {
	const bridge, overlay = "mb-ws1-abc", "mb-ws1-abc-ov"
	app := docker.Container{ID: "app1", Networks: []docker.ContainerNetwork{
		{Name: bridge, IPAddress: "10.64.0.2", Aliases: []string{"mb-app-tok-7"}},
		{Name: "miabi", IPAddress: "10.65.0.2", Aliases: []string{"mb-app-tok-7"}},
	}}
	db := docker.Container{ID: "db1", Networks: []docker.ContainerNetwork{
		{Name: bridge, IPAddress: "10.64.0.3", Aliases: []string{"mb-db-tok-3"}},
	}}
	dc := &fakeDocker{
		containers: []docker.Container{{ID: "app1"}, {ID: "db1"}},
		inspect:    map[string]docker.Container{"app1": app, "db1": db},
	}

	s := &Service{}
	if err := s.moveContainers(context.Background(), dc, bridge, overlay); err != nil {
		t.Fatalf("moveContainers: %v", err)
	}

	if len(dc.calls) != 4 {
		t.Fatalf("want 4 calls (connect+disconnect per container), got %d: %+v", len(dc.calls), dc.calls)
	}
	for _, id := range []string{"app1", "db1"} {
		ci, di := indexOf(dc.calls, "connect", id), indexOf(dc.calls, "disconnect", id)
		if ci < 0 || di < 0 {
			t.Fatalf("%s: missing connect(%d)/disconnect(%d): %+v", id, ci, di, dc.calls)
		}
		if ci > di {
			t.Errorf("%s: disconnected from the bridge before connecting to the overlay — "+
				"the container would be briefly stranded off-network", id)
		}
		if got := dc.calls[ci].network; got != overlay {
			t.Errorf("%s: connected to %q, want the overlay %q", id, got, overlay)
		}
		if got := dc.calls[di].network; got != bridge {
			t.Errorf("%s: disconnected from %q, want the bridge %q", id, got, bridge)
		}
	}
	// Aliases must be carried across verbatim, not recomputed.
	if got := dc.calls[indexOf(dc.calls, "connect", "app1")].aliases; len(got) != 1 || got[0] != "mb-app-tok-7" {
		t.Errorf("app aliases not carried to the overlay: got %v, want [mb-app-tok-7]", got)
	}
	if got := dc.calls[indexOf(dc.calls, "connect", "db1")].aliases; len(got) != 1 || got[0] != "mb-db-tok-3" {
		t.Errorf("database aliases not carried to the overlay: got %v, want [mb-db-tok-3] — "+
			"every connection URI addresses the instance by this alias", got)
	}
}

// A container not on the workspace bridge must be left completely alone.
func TestMoveContainersIgnoresUnattached(t *testing.T) {
	dc := &fakeDocker{
		containers: []docker.Container{{ID: "other"}},
		inspect: map[string]docker.Container{"other": {ID: "other", Networks: []docker.ContainerNetwork{
			{Name: "some-other-net", IPAddress: "172.17.0.2"},
		}}},
	}
	s := &Service{}
	if err := s.moveContainers(context.Background(), dc, "mb-ws1-abc", "mb-ws1-abc-ov"); err != nil {
		t.Fatalf("moveContainers: %v", err)
	}
	if len(dc.calls) != 0 {
		t.Fatalf("touched a container that was not on the workspace bridge: %+v", dc.calls)
	}
}

// A failed connect must abort before disconnect, or the container ends up on no network.
func TestMoveContainersDoesNotDisconnectWhenConnectFails(t *testing.T) {
	const bridge = "mb-ws1-abc"
	dc := &fakeDocker{
		containers: []docker.Container{{ID: "app1"}},
		inspect: map[string]docker.Container{"app1": {ID: "app1", Networks: []docker.ContainerNetwork{
			{Name: bridge, IPAddress: "10.64.0.2", Aliases: []string{"mb-app-tok-7"}},
		}}},
		connectErr: errors.New("overlay unreachable"),
	}
	s := &Service{}
	if err := s.moveContainers(context.Background(), dc, bridge, bridge+"-ov"); err == nil {
		t.Fatal("want an error when the overlay connect fails, got nil")
	}
	for _, c := range dc.calls {
		if c.op == "disconnect" {
			t.Fatal("disconnected from the bridge even though the overlay connect failed — " +
				"the container would be left with no network")
		}
	}
}

// The peer name must be deterministic so a rerun leaks no networks, and round-trip so Disable can restore the bridge.
func TestPeerNameIsDeterministicAndRoundTrips(t *testing.T) {
	const bridge = "mb-ws1-abc"
	overlay := peerName(bridge, DriverOverlay)

	if overlay == bridge {
		t.Fatal("the overlay must not reuse the bridge's name — both exist at once mid-migration")
	}
	if again := peerName(bridge, DriverOverlay); again != overlay {
		t.Fatalf("not deterministic: %q vs %q — a re-run would leak a second network", overlay, again)
	}
	if back := peerName(overlay, DriverBridge); back != bridge {
		t.Fatalf("does not round-trip: %q -> %q -> %q; disabling cluster mode could not restore the bridge",
			bridge, overlay, back)
	}
	// Idempotent both ways, so a resumed run converges instead of appending a second suffix.
	if got := peerName(overlay, DriverOverlay); got != overlay {
		t.Errorf("converting an overlay to an overlay renamed it: %q -> %q", overlay, got)
	}
	if got := peerName(bridge, DriverBridge); got != bridge {
		t.Errorf("converting a bridge to a bridge renamed it: %q -> %q", bridge, got)
	}
}

// Rollback needs a live swarm to detach containers from an overlay.
func TestRollbackRefusesWithoutCluster(t *testing.T) {
	if _, err := (&Service{}).Rollback(context.Background()); !errors.Is(err, ErrClusterRequired) {
		t.Fatalf("want ErrClusterRequired, got %v", err)
	}
}

// A non-Attachable overlay cannot be joined by plain containers, i.e. every app and database.
func TestNetworkSpecOverlayIsAttachableAndEncrypted(t *testing.T) {
	spec := networkSpec("mb-ws1-abc-ov", DriverOverlay, false)
	if !spec.Attachable {
		t.Error("overlay is not Attachable: apps and databases are plain containers, not swarm " +
			"services, and could not join it")
	}
	if !spec.Encrypted {
		t.Error("overlay is not Encrypted: it carries east-west traffic between nodes and there is " +
			"no WireGuard underlay beneath it")
	}
	if got := allocKind(DriverOverlay); got != models.NetAllocKindOverlay {
		t.Errorf("allocKind(overlay) = %q, want %q", got, models.NetAllocKindOverlay)
	}

	bridge := networkSpec("mb-ws1-abc", DriverBridge, false)
	if bridge.Attachable || bridge.Encrypted {
		t.Error("a bridge must not carry overlay-only options")
	}
	if got := allocKind(DriverBridge); got != models.NetAllocKindWorkspace {
		t.Errorf("allocKind(bridge) = %q, want %q", got, models.NetAllocKindWorkspace)
	}
}

// Single-node installs never get an overlay; an explicit driver from the caller always wins.
func TestDriverFor(t *testing.T) {
	tests := []struct {
		name      string
		clusterOn bool
		explicit  string
		want      string
		wantErr   bool
	}{
		{name: "no cluster: default is a node-local bridge", want: DriverBridge},
		{name: "cluster: default spans nodes", clusterOn: true, want: DriverOverlay},
		{name: "explicit bridge wins in cluster mode", clusterOn: true, explicit: DriverBridge, want: DriverBridge},
		{name: "explicit overlay is allowed", explicit: DriverOverlay, want: DriverOverlay},
		{name: "unknown driver is rejected", explicit: "vxlan", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{}
			if tt.clusterOn {
				s.SetCluster(fakeCluster{on: true})
			}
			got, err := s.driverFor(tt.explicit)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidDriver) {
					t.Fatalf("want ErrInvalidDriver, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("driverFor: %v", err)
			}
			if got != tt.want {
				t.Errorf("driverFor(%q) = %q, want %q", tt.explicit, got, tt.want)
			}
		})
	}
}

// A nil cluster service must never report cluster mode, keeping plain Docker on plain bridges.
func TestClusterOffByDefault(t *testing.T) {
	if (&Service{}).clusterOn() {
		t.Fatal("cluster mode reported on with no cluster service wired")
	}
}

// Without swarm Migrate would destroy the bridges and be unable to replace them.
func TestMigrateRefusesWithoutCluster(t *testing.T) {
	if _, err := (&Service{}).Migrate(context.Background()); !errors.Is(err, ErrClusterRequired) {
		t.Fatalf("want ErrClusterRequired, got %v", err)
	}
}

func indexOf(calls []call, op, container string) int {
	for i, c := range calls {
		if c.op == op && c.container == container {
			return i
		}
	}
	return -1
}
