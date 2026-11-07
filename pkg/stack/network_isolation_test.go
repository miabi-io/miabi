// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/pkg/stack/docker"
	"gopkg.in/yaml.v3"
)

// Routed apps join m.Network, and neither Postgres's superuser password nor Redis's queue has a second boundary.
func TestPlatformComponentsAreOffTheProxyNetwork(t *testing.T) {
	m := testManifest()
	svc := &Service{}

	for _, c := range svc.components(m) {
		spec := c.Build(m, c.Name, *c.Image(m))
		onProxy := slices.Contains(spec.Networks, m.Network.Name)
		onPrivate := slices.Contains(spec.Networks, m.InternalNetwork.Name)

		if !onPrivate {
			t.Errorf("%s is not on %s — it cannot reach the rest of the stack", c.Name, m.InternalNetwork.Name)
		}
		if c.Name == ContainerGateway {
			if !onProxy {
				t.Errorf("%s left %s — it could no longer reach any app backend, and every route would 502",
					c.Name, m.Network.Name)
			}
			continue
		}
		if onProxy {
			t.Errorf("%s is on %s, the shared app network — any container with a route could dial it",
				c.Name, m.Network.Name)
		}
	}
}

// The gateway being the only bridge is what makes the proxy network ingress-only.
func TestOnlyTheGatewayBridgesBothNetworks(t *testing.T) {
	m := testManifest()
	svc := &Service{}

	var bridging []string
	for _, c := range svc.components(m) {
		spec := c.Build(m, c.Name, *c.Image(m))
		if slices.Contains(spec.Networks, m.Network.Name) && slices.Contains(spec.Networks, m.InternalNetwork.Name) {
			bridging = append(bridging, c.Name)
		}
	}
	if len(bridging) != 1 || bridging[0] != ContainerGateway {
		t.Errorf("components on both networks = %v, want only %s", bridging, ContainerGateway)
	}
}

func TestPreSplitManifestGainsThePrivateNetwork(t *testing.T) {
	m := &Manifest{
		Version: CurrentVersion,
		Domain:  "miabi.example.com",
		Network: NetworkConfig{Name: DefaultNetwork, Subnet: DefaultSubnet},
		Images:  Images{Miabi: "miabi/miabi:1.8.0"},
	}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if m.InternalNetwork.Name != DefaultInternalNetwork || m.InternalNetwork.Subnet != DefaultInternalSubnet {
		t.Errorf("internal network = %q (%s), want %q (%s)",
			m.InternalNetwork.Name, m.InternalNetwork.Subnet, DefaultInternalNetwork, DefaultInternalSubnet)
	}
}

// A naive 10.64.0.0/16 would land on the workspace allocator's pool (10.64.0.0/12).
func TestDefaultSubnetsDoNotCollide(t *testing.T) {
	if DefaultInternalSubnet == DefaultSubnet {
		t.Fatalf("both networks default to %s", DefaultSubnet)
	}
	if strings.HasPrefix(DefaultInternalSubnet, "10.6") {
		if oct := strings.Split(DefaultInternalSubnet, ".")[1]; oct == "64" {
			t.Errorf("the private subnet defaults into the workspace allocator's 10.64.0.0/12 pool")
		}
	}
}

// Pointing both names at one network would silently put every component back on the proxy network.
func TestNormalizeRefusesOneNetworkUnderTwoNames(t *testing.T) {
	m := testManifest()
	m.InternalNetwork.Name = m.Network.Name
	if err := m.Normalize(); err == nil {
		t.Error("Normalize accepted internal_network.name == network.name")
	}
}

// Without networks in the spec hash, converge would leave Postgres on the app network.
func TestMovingAComponentBetweenNetworksRecreatesIt(t *testing.T) {
	m := testManifest()
	before := specHash(postgresSpec(m, ContainerPostgres, m.Images.Postgres))

	old := *m
	old.InternalNetwork = old.Network
	if specHash(postgresSpec(&old, ContainerPostgres, old.Images.Postgres)) == before {
		t.Error("the network set does not reach the spec hash — a converge would report 'up to date' " +
			"and leave the database on the shared app network")
	}
}

// Attach before recreating, or a single-component upgrade leaves the control plane cut off from its database.
func TestMigrateNetworksAttachesTheRunningStackWithoutRecreatingIt(t *testing.T) {
	m := testManifest()
	dc := &fakeEngine{containers: map[string][]string{
		ContainerPostgres:     {DefaultNetwork},
		ContainerRedis:        {DefaultNetwork},
		ContainerControlPlane: {DefaultNetwork},
		ContainerGateway:      {DefaultNetwork},
	}}
	svc := New(dc, nil, "/etc/miabi/miabi.yaml")

	if err := svc.migrateNetworks(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ContainerPostgres, ContainerRedis, ContainerControlPlane, ContainerGateway} {
		if !slices.Contains(dc.connected[name], DefaultInternalNetwork) {
			t.Errorf("%s was not attached to %s before any recreate", name, DefaultInternalNetwork)
		}
	}
	if len(dc.removed) > 0 {
		t.Errorf("the migration recreated %v — it must move a RUNNING stack, not restart it", dc.removed)
	}
	// The gateway belongs on both, so it must not be detached from the one it still needs.
	if slices.Contains(dc.disconnected[ContainerGateway], DefaultNetwork) {
		t.Error("the gateway was detached from the app network — every route would 502")
	}
}

// A converge interrupted between attach and recreate leaves a hash-current container with a forbidden attachment.
func TestMigrateNetworksDetachesAHalfMigratedComponent(t *testing.T) {
	m := testManifest()
	dc := &fakeEngine{containers: map[string][]string{
		ContainerPostgres: {DefaultNetwork, DefaultInternalNetwork},
	}}
	svc := New(dc, nil, "/etc/miabi/miabi.yaml")

	if err := svc.migrateNetworks(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(dc.disconnected[ContainerPostgres], DefaultNetwork) {
		t.Errorf("postgres kept its %s attachment — a routed app could still dial it", DefaultNetwork)
	}
}

func TestMigrateNetworksIsANoOpOnAFreshHost(t *testing.T) {
	m := testManifest()
	dc := &fakeEngine{}
	svc := New(dc, nil, "/etc/miabi/miabi.yaml")

	if err := svc.migrateNetworks(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if len(dc.connected) > 0 || len(dc.disconnected) > 0 {
		t.Errorf("touched a stack that does not exist: connected=%v disconnected=%v", dc.connected, dc.disconnected)
	}
}

type fakeEngine struct {
	containers   map[string][]string // name -> attached networks
	connected    map[string][]string
	disconnected map[string][]string
	removed      []string
}

func (f *fakeEngine) InspectContainer(_ context.Context, id string) (docker.Container, error) {
	nets, ok := f.containers[id]
	if !ok {
		return docker.Container{}, docker.ErrNotFound
	}
	c := docker.Container{Names: []string{"/" + id}}
	for _, n := range nets {
		c.Networks = append(c.Networks, docker.ContainerNetwork{Name: n})
	}
	return c, nil
}

func (f *fakeEngine) NetworkConnect(_ context.Context, name, containerID string, _ []string) error {
	if f.connected == nil {
		f.connected = map[string][]string{}
	}
	f.connected[containerID] = append(f.connected[containerID], name)
	f.containers[containerID] = append(f.containers[containerID], name)
	return nil
}

func (f *fakeEngine) NetworkDisconnect(_ context.Context, name, containerID string, _ bool) error {
	if f.disconnected == nil {
		f.disconnected = map[string][]string{}
	}
	f.disconnected[containerID] = append(f.disconnected[containerID], name)
	return nil
}

func (f *fakeEngine) RemoveContainer(_ context.Context, id string, _ bool) error {
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeEngine) ListContainers(context.Context, bool) ([]docker.Container, error) {
	return nil, nil
}

func (f *fakeEngine) InspectContainerConfig(context.Context, string) (docker.ContainerConfig, error) {
	return docker.ContainerConfig{}, docker.ErrNotFound
}
func (f *fakeEngine) RunContainer(context.Context, docker.RunSpec) (string, error) { return "", nil }
func (f *fakeEngine) RestartContainer(context.Context, string, int) error          { return nil }
func (f *fakeEngine) RunOneShot(context.Context, docker.RunSpec) (int, string, error) {
	return 0, "", nil
}
func (f *fakeEngine) PullImage(context.Context, string, *docker.RegistryAuth) error { return nil }
func (f *fakeEngine) ImageExists(context.Context, string) (bool, error)             { return true, nil }
func (f *fakeEngine) EnsureNetworkSpec(context.Context, docker.NetworkSpec) (string, error) {
	return "", nil
}
func (f *fakeEngine) CreateVolume(context.Context, string, map[string]string, int64) (docker.Volume, error) {
	return docker.Volume{}, nil
}
func (f *fakeEngine) InspectVolume(context.Context, string) (docker.Volume, error) {
	return docker.Volume{}, docker.ErrNotFound
}
func (f *fakeEngine) RemoveVolume(context.Context, string, bool) error { return nil }
func (f *fakeEngine) InspectNetwork(context.Context, string) (docker.NetworkState, error) {
	return docker.NetworkState{}, docker.ErrNotFound
}
func (f *fakeEngine) RemoveNetwork(context.Context, string) error { return nil }
func (f *fakeEngine) Rootless(context.Context) (bool, error)      { return false, nil }

// A Compose file putting Postgres back on the shared network would expose the control-plane DB to every app.
func TestComposeStacksKeepThePlatformOffTheSharedNetwork(t *testing.T) {
	for _, tc := range []struct {
		file    string
		gateway string // the service that bridges both networks
	}{
		{"../../examples/compose/compose.yaml", "gateway"},
		{"../../examples/compose/compose.traefik.yaml", "traefik"},
	} {
		t.Run(filepath.Base(tc.file), func(t *testing.T) {
			b, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			var f struct {
				Services map[string]struct {
					Networks []string `yaml:"networks"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal(b, &f); err != nil {
				t.Fatal(err)
			}

			for _, svc := range []string{"miabi-postgres", "miabi-redis", "miabi"} {
				nets := f.Services[svc].Networks
				if len(nets) == 0 {
					t.Fatalf("%s declares no networks — did the service get renamed?", svc)
				}
				if slices.Contains(nets, "miabi-proxy") {
					t.Errorf("%s is on the shared app network (%v) — any container with a route could dial it", svc, nets)
				}
				if !slices.Contains(nets, "miabi-internal") {
					t.Errorf("%s is not on the private network (%v)", svc, nets)
				}
			}

			gw := f.Services[tc.gateway].Networks
			if !slices.Contains(gw, "miabi-proxy") || !slices.Contains(gw, "miabi-internal") {
				t.Errorf("%s is on %v, want both networks — it is the only bridge between them", tc.gateway, gw)
			}
		})
	}
}

// Without this key, out-of-process helpers (backups, registry) stay on the shared network away from the database.
func TestComposeEnvExampleNamesThePrivateNetwork(t *testing.T) {
	b, err := os.ReadFile("../../examples/compose/.env.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\nMIABI_INTERNAL_NETWORK=miabi-internal") {
		t.Error("examples/compose/.env.example does not set MIABI_INTERNAL_NETWORK=miabi-internal — " +
			"a stack brought up from it would run platform backups against a network its database left")
	}
}
