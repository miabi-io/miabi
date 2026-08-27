// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

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

// The whole point of the split: a routed application joins m.Network, so anything else sitting there is
// reachable from a tenant container. Postgres holds one superuser password for the entire control plane
// and Redis's queue is code execution in the worker — neither has a second boundary to fall back on.
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

// The gateway is the ONLY component on both, and that is what makes the proxy network ingress-only:
// traffic crosses into the platform at a process that terminates TLS and applies the operator's
// middlewares, not at a flat Docker bridge.
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

// Normalize supplies the private network for every manifest written before the split, so an existing
// install gains it on its next converge without the operator editing anything.
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

// The default private subnet must miss both neighbours: the proxy network's own range, and the workspace
// allocator's pool (MIABI_NETWORK_POOL_CIDR = 10.64.0.0/12), whose first block a naive 10.64.0.0/16
// would land squarely on.
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

// A hand-edited manifest that points both names at one network would report a private stack it does not
// have — every component back on the proxy network, silently.
func TestNormalizeRefusesOneNetworkUnderTwoNames(t *testing.T) {
	m := testManifest()
	m.InternalNetwork.Name = m.Network.Name
	if err := m.Normalize(); err == nil {
		t.Error("Normalize accepted internal_network.name == network.name")
	}
}

// Networks are part of the spec hash, which is what makes the split actually happen on an existing
// install: without this, converge would decide "up to date" and leave Postgres on the app network.
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

// The migration must ATTACH before anything is recreated. Converge recreates in dependency order, so a
// stack that changed networks one container at a time would spend the gap with a control plane talking
// to a database that had just left its network — and `miabi upgrade miabi-postgres` recreates exactly
// one component, leaving the panel down until someone ran a full converge.
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

// The component loop leaves a container alone when its spec hash is already current, which is exactly
// the state a converge interrupted between the attach and the recreate leaves behind. Without the
// disconnect, that container keeps an attachment the manifest says it must not have.
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

// A fresh install has nothing to migrate: the component loop creates every container on the right
// networks, and touching a missing one would be an error the installer has no business raising.
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

// fakeEngine records the network calls migrateNetworks makes. Every other method is enough to satisfy
// docker.Client and no more — this exercise is about attachments, not about running containers.
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

// The Compose stacks are the other install path, and they drifted apart once already — which is why
// the goma.yml drift test exists. The split is worth the same guard: a Compose file that quietly puts
// Postgres back on the shared app network would hand every routed container the control-plane database,
// and nothing in Go would notice.
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
				if slices.Contains(nets, "miabi") {
					t.Errorf("%s is on the shared app network (%v) — any container with a route could dial it", svc, nets)
				}
				if !slices.Contains(nets, "miabi-internal") {
					t.Errorf("%s is not on the private network (%v)", svc, nets)
				}
			}

			gw := f.Services[tc.gateway].Networks
			if !slices.Contains(gw, "miabi") || !slices.Contains(gw, "miabi-internal") {
				t.Errorf("%s is on %v, want both networks — it is the only bridge between them", tc.gateway, gw)
			}
		})
	}
}

// Compose declares the private network, but the CONTROL PLANE only learns its name from the
// environment. Without this key the helper containers Miabi runs out of process — platform backups,
// the built-in registry — stay on the shared network, where the database no longer is.
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
