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

//go:build integration

package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// ensureNetwork creates a bridge network and registers its removal.
func ensureNetwork(t *testing.T, cli Client, name string) string {
	t.Helper()
	ctx := bg(t, 20*time.Second)
	id, err := cli.EnsureNetwork(ctx, name)
	if err != nil {
		t.Fatalf("EnsureNetwork(%s): %v", name, err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = cli.RemoveNetwork(c, name)
	})
	return id
}

// TestNetworkCRUD covers create-with-subnet, list, and remove.
func TestNetworkCRUD(t *testing.T) {
	cli := newClient(t)
	name := uniqueName("net")
	ctx := bg(t, 20*time.Second)

	id, err := cli.CreateNetworkSpec(ctx, NetworkSpec{
		Name:   name,
		Driver: "bridge",
		Subnet: "10.199.222.0/24",
		Labels: map[string]string{LabelManaged: "true"},
	})
	if err != nil {
		t.Fatalf("CreateNetworkSpec: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = cli.RemoveNetwork(c, name)
	})
	if id == "" {
		t.Fatal("CreateNetworkSpec returned empty id")
	}

	nets, err := cli.ListNetworks(ctx)
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	var found *Network
	for i := range nets {
		if nets[i].Name == name {
			found = &nets[i]
		}
	}
	if found == nil {
		t.Fatalf("network %q not in ListNetworks", name)
	}
	if found.Subnet != "10.199.222.0/24" {
		t.Errorf("subnet: got %q, want 10.199.222.0/24", found.Subnet)
	}

	if err := cli.RemoveNetwork(ctx, name); err != nil {
		t.Fatalf("RemoveNetwork: %v", err)
	}
}

// A dropped alias silently breaks app->database addressing.
func TestNetworkAliasResolves(t *testing.T) {
	cli := newClient(t)
	net := uniqueName("net")
	ensureNetwork(t, cli, net)

	// Target advertises the alias "db" on the network.
	run(t, cli, RunSpec{
		Image:          imgAlpine,
		Cmd:            []string{"sleep", "3600"},
		Networks:       []string{net},
		NetworkAliases: []string{"db"},
		RestartPolicy:  "no",
		Labels:         map[string]string{LabelApp: "it"},
	})
	// Peer on the same network resolves it.
	peer := run(t, cli, RunSpec{
		Image:         imgAlpine,
		Cmd:           []string{"sleep", "3600"},
		Networks:      []string{net},
		RestartPolicy: "no",
		Labels:        map[string]string{LabelApp: "it"},
	})

	out := execOutput(t, cli, peer, "ping", "-c", "1", "-W", "2", "db")
	if !pingReached(out) {
		t.Errorf("alias 'db' did not resolve/reach from peer; ping said:\n%s", out)
	}
}

// busybox nslookup expands single-label names against the host search domain; musl's ping also tries the bare name.
func pingReached(out string) bool {
	return strings.Contains(out, "bytes from")
}

// Tenant isolation is a security property: B cannot even resolve an alias on A.
func TestNetworkIsolation(t *testing.T) {
	cli := newClient(t)
	netA := uniqueName("neta")
	netB := uniqueName("netb")
	ensureNetwork(t, cli, netA)
	ensureNetwork(t, cli, netB)

	run(t, cli, RunSpec{
		Image:          imgAlpine,
		Cmd:            []string{"sleep", "3600"},
		Networks:       []string{netA},
		NetworkAliases: []string{"secret-db"},
		RestartPolicy:  "no",
		Labels:         map[string]string{LabelApp: "it"},
	})
	intruder := run(t, cli, RunSpec{
		Image:         imgAlpine,
		Cmd:           []string{"sleep", "3600"},
		Networks:      []string{netB},
		RestartPolicy: "no",
		Labels:        map[string]string{LabelApp: "it"},
	})

	out := execOutput(t, cli, intruder, "ping", "-c", "1", "-W", "2", "secret-db")
	if pingReached(out) {
		t.Errorf("cross-network isolation breached: 'secret-db' reachable from another network:\n%s", out)
	}

	// Proves the prior failure was isolation, not a broken alias.
	ctx := bg(t, 20*time.Second)
	if err := cli.NetworkConnect(ctx, netA, intruder, []string{"intruder"}); err != nil {
		t.Fatalf("NetworkConnect: %v", err)
	}
	out = execOutput(t, cli, intruder, "ping", "-c", "1", "-W", "2", "secret-db")
	if !pingReached(out) {
		t.Errorf("after NetworkConnect, 'secret-db' still not reachable:\n%s", out)
	}
	if err := cli.NetworkDisconnect(ctx, netA, intruder, true); err != nil {
		t.Errorf("NetworkDisconnect: %v", err)
	}
}

func TestVolumeBackupRoundTrip(t *testing.T) {
	cli := newClient(t)
	ensureImage(t, cli, imgAlpine)
	name := uniqueName("vol")
	ctx := bg(t, 60*time.Second)

	vol, err := cli.CreateVolume(ctx, name, map[string]string{LabelManaged: "true"}, 0)
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = cli.RemoveVolume(c, name, true)
	})
	if vol.Name != name {
		t.Errorf("CreateVolume name: got %q, want %q", vol.Name, name)
	}
	if _, err := cli.InspectVolume(ctx, name); err != nil {
		t.Fatalf("InspectVolume: %v", err)
	}

	payload := []byte("miabi-backup-roundtrip\n" + strings.Repeat("x", 4096))
	if err := cli.CopyToVolume(ctx, name, imgAlpine, "dump.bin", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("CopyToVolume: %v", err)
	}

	rc, size, err := cli.CopyFileFromVolume(ctx, name, imgAlpine, "dump.bin")
	if err != nil {
		t.Fatalf("CopyFileFromVolume: %v", err)
	}
	defer rc.Close()
	if size != int64(len(payload)) {
		t.Errorf("read-back size: got %d, want %d", size, len(payload))
	}
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Errorf("read-back content mismatch: got %d bytes, want %d", len(got), len(payload))
	}

	// A missing file is ErrNotFound, not a generic error.
	if _, _, err := cli.CopyFileFromVolume(ctx, name, imgAlpine, "nope.bin"); !errors.Is(err, ErrNotFound) {
		t.Errorf("CopyFileFromVolume(missing): got %v, want ErrNotFound", err)
	}
}

// DialNetwork is a hijacked attach stream used as a net.Conn; nothing else in Miabi would catch it breaking.
func TestDialNetworkSpeaksPostgres(t *testing.T) {
	cli := newClient(t)
	ensureImage(t, cli, imgSocat)
	ensureImage(t, cli, imgPostgres)
	net := uniqueName("pgnet")
	ensureNetwork(t, cli, net)

	run(t, cli, RunSpec{
		Image:          imgPostgres,
		Env:            []string{"POSTGRES_PASSWORD=miabi-it"},
		Networks:       []string{net},
		NetworkAliases: []string{"pg"},
		RestartPolicy:  "no",
		Labels:         map[string]string{LabelApp: "it"},
	})

	// SSLRequest is length=8, code=80877103; the server replies 'S' or 'N' once it accepts connections.
	sslRequest := make([]byte, 8)
	binary.BigEndian.PutUint32(sslRequest[0:4], 8)
	binary.BigEndian.PutUint32(sslRequest[4:8], 80877103)

	deadline := time.Now().Add(60 * time.Second)
	var reply byte
	var lastErr error
	for time.Now().Before(deadline) {
		ctx := bg(t, 15*time.Second)
		conn, err := cli.DialNetwork(ctx, net, imgSocat, "pg", 5432)
		if err != nil {
			lastErr = err
			time.Sleep(time.Second)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(sslRequest); err != nil {
			lastErr = err
			conn.Close()
			time.Sleep(time.Second)
			continue
		}
		b := make([]byte, 1)
		_, rerr := io.ReadFull(conn, b)
		conn.Close()
		if rerr != nil {
			lastErr = rerr
			time.Sleep(time.Second)
			continue
		}
		reply = b[0]
		break
	}
	if reply != 'S' && reply != 'N' {
		t.Fatalf("no Postgres SSLRequest reply through the relay (last error: %v)", lastErr)
	}

	assertNoRelayContainers(t, cli)
}

// A leaked relay is a leaked container per outbound dial.
func assertNoRelayContainers(t *testing.T, cli Client) {
	t.Helper()
	ctx := bg(t, 20*time.Second)
	// Relays self-remove on socat exit + on Close; allow a brief settle.
	for i := 0; i < 10; i++ {
		cs, err := cli.ListContainers(ctx, true)
		if err != nil {
			t.Fatalf("ListContainers: %v", err)
		}
		leaked := false
		for _, c := range cs {
			for _, n := range c.Names {
				if strings.Contains(n, "mb-fwd-") {
					leaked = true
				}
			}
		}
		if !leaked {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Error("a DialNetwork relay container (mb-fwd-*) was still present after Close")
}
