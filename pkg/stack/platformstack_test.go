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
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/pkg/stack/docker"
)

// The CLI and Compose paths must ship the same goma.yml, or they route differently.
func TestEmbeddedGomaConfigMatchesTheComposeOne(t *testing.T) {
	onDisk, err := os.ReadFile(filepath.Join("..", "..", "examples", "compose", "goma.yml"))
	if err != nil {
		t.Fatalf("read examples/compose/goma.yml: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(onDisk), bytes.TrimSpace(GomaConfig())) {
		t.Error("assets/goma.yml has drifted from examples/compose/goma.yml — " +
			"the CLI install and the Compose install would serve different routing rules. " +
			"Copy examples/compose/goma.yml over stack/assets/goma.yml.")
	}
}

// Git tags carry a leading "v" but Docker tags don't; the bad ref would only fail at pull time.
func TestDefaultImageTagsAreDockerTagsNotGitTags(t *testing.T) {
	for _, ref := range []string{
		DefaultPostgresImage, DefaultRedisImage, DefaultGatewayImage, DefaultRunnerImage,
	} {
		_, tag, ok := strings.Cut(ref, ":")
		if !ok {
			t.Errorf("%s has no tag — it would float on :latest", ref)
			continue
		}
		if strings.HasPrefix(tag, "v") {
			t.Errorf("%s: docker tags drop the leading v (install.sh does ${VERSION#v}) — "+
				"this image reference does not exist", ref)
		}
	}
}

func testManifest() *Manifest {
	m := Defaults("miabi/miabi:1.4.0")
	m.Domain = "miabi.example.com"
	if err := m.Normalize(); err != nil {
		panic(err)
	}
	return m
}

// Go map iteration is random, and an unstable hash would recreate the whole stack, restarting Postgres.
func TestSpecHashIsStableAcrossRuns(t *testing.T) {
	m := testManifest()
	first := specHash(controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi))
	for i := 0; i < 50; i++ {
		got := specHash(controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi))
		if got != first {
			t.Fatalf("spec hash is not stable: run %d gave %s, want %s "+
				"(every converge would recreate the stack)", i, got, first)
		}
	}
}

// ...but it must still notice a real change, or an update would silently no-op.
func TestSpecHashChangesWithTheThingsThatMatter(t *testing.T) {
	m := testManifest()
	base := specHash(controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi))

	t.Run("image", func(t *testing.T) {
		if specHash(controlPlaneSpec(m, ContainerControlPlane, "miabi/miabi:9.9.9")) == base {
			t.Error("a new image did not change the hash — `miabi update` would do nothing")
		}
	})

	// testManifest() generates fresh secrets, so two independent manifests would differ for unrelated reasons.
	t.Run("rotated secret", func(t *testing.T) {
		m2 := *m
		m2.Secrets.DBPassword = "rotated"
		if specHash(controlPlaneSpec(&m2, ContainerControlPlane, m2.Images.Miabi)) == base {
			t.Error("rotating the database password did not change the hash — the container " +
				"would keep the old password and lock itself out of its own database")
		}
	})

	t.Run("domain", func(t *testing.T) {
		gwBase := specHash(gatewaySpec(m, ContainerGateway, m.Images.Gateway))
		m2 := *m
		m2.Domain = "other.example.com"
		if specHash(gatewaySpec(&m2, ContainerGateway, m2.Images.Gateway)) == gwBase {
			t.Error("changing the domain did not change the gateway's hash — " +
				"the gateway would keep serving the old hostname")
		}
	})
}

// Two containers cannot both bind :443, so the rollout's test container must not publish ports.
func TestOnlyTheLiveGatewayPublishesPorts(t *testing.T) {
	m := testManifest()

	live := gatewaySpec(m, ContainerGateway, m.Images.Gateway)
	if len(live.Ports) == 0 {
		t.Error("the live gateway publishes no ports — nothing would reach the panel")
	}

	test := gatewaySpec(m, ContainerGateway+"-test", m.Images.Gateway)
	if len(test.Ports) != 0 {
		t.Errorf("the test gateway publishes %v — it would collide with the live one on :443 "+
			"and every update would fail", test.Ports)
	}
}

// Import and destructive-op guards read these labels; an unlabeled component would be importable and stoppable.
func TestEveryComponentIsLabeledAsPlatform(t *testing.T) {
	m := testManifest()
	s := &Service{}

	for _, c := range s.components(m) {
		spec := c.Build(m, c.Name, *c.Image(m))
		t.Run(c.Name, func(t *testing.T) {
			if !docker.IsPlatformStack(spec.Labels) {
				t.Error("not part-of=miabi — it would be offered in the import list")
			}
			if !docker.IsProtected(spec.Labels) {
				t.Error("not protected — it could be stopped from the containers page")
			}
			if got := docker.ManagedBy(spec.Labels); got != docker.ManagedByMiabi {
				t.Errorf("managed-by=%q, want %q — the UI would refuse to update it", got, docker.ManagedByMiabi)
			}
		})
	}
}

// Registry, agents and edge gateways are Miabi but not the stack the CLI installs or updates.
func TestDiscoverKeepsPlatformInfraOutOfTheStack(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   bool // true = it is a stack component
	}{
		{"miabi", docker.PlatformLabels(docker.RoleControlPlane, docker.ManagedByMiabi, nil), true},
		{"miabi-postgres", docker.PlatformLabels(docker.RolePlatformDB, docker.ManagedByMiabi, nil), true},
		{"miabi-redis", docker.PlatformLabels(docker.RolePlatformCache, docker.ManagedByMiabi, nil), true},
		{"miabi-gateway", docker.PlatformLabels(docker.RoleGateway, docker.ManagedByCompose, nil), true},
		// Compose renames them; only the label can find these.
		{"miabi-miabi-postgres-1", docker.PlatformLabels(docker.RolePlatformDB, docker.ManagedByCompose, nil), true},

		// Platform, but NOT the stack.
		{"mb-registry", docker.PlatformLabels(docker.RoleRegistry, docker.ManagedByMiabi, nil), false},
		{"mb-node-gateway", docker.PlatformLabels(docker.RoleNodeGateway, docker.ManagedByMiabi, nil), false},
		{"miabi-agent", docker.PlatformLabels(docker.RoleAgent, docker.ManagedByExternal, nil), false},

		// Not ours at all.
		{"blog-db-1", map[string]string{"com.docker.compose.project": "blog"}, false},
		{"mb-app-x", map[string]string{docker.LabelApp: "3"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			role, _ := docker.LabelValue(c.labels, docker.LabelRole)
			got := docker.IsPlatformStack(c.labels) && stackRoles[role]
			if !got {
				// the unlabeled-name fallback
				_, known := stackNames[c.name]
				got = known && !docker.IsManaged(c.labels)
			}
			if got != c.want {
				t.Errorf("in stack = %v, want %v", got, c.want)
			}
		})
	}
}

// The check runs before anything is created; our own gateway holding :80/:443 on reinstall is not a conflict.
func TestPortConflictIgnoresOurOwnGateway(t *testing.T) {
	held := func(name string, ports ...uint16) docker.Container {
		c := docker.Container{Names: []string{"/" + name}, State: "running"}
		for _, p := range ports {
			c.Ports = append(c.Ports, docker.Port{PrivatePort: p, PublicPort: p, Protocol: "tcp"})
		}
		return c
	}

	cases := []struct {
		name       string
		containers []docker.Container
		wantHolder string // "" = no conflict
	}{
		{
			"a reinstall: our own gateway holds the ports",
			[]docker.Container{held(ContainerGateway, 80, 443)},
			"",
		},
		{
			"a rollout died and left its test container holding them",
			[]docker.Container{held(ContainerGateway+"-test", 80, 443)},
			"",
		},
		{
			"someone else's proxy",
			[]docker.Container{held("traefik", 80, 443)},
			"traefik",
		},
		{
			// A Compose install's gateway is project-prefixed, so it is not ContainerGateway.
			"an existing Compose install",
			[]docker.Container{held("miabi-gateway-1", 443)},
			"miabi-gateway-1",
		},
		{
			"a container on an unrelated port",
			[]docker.Container{held("postgres", 5432)},
			"",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The bind probe needs a live engine and is exercised end-to-end.
			holder := map[int]string{}
			for _, cont := range c.containers {
				for _, p := range cont.Ports {
					if p.PublicPort != 0 {
						holder[int(p.PublicPort)] = containerName(cont)
					}
				}
			}
			got := ""
			for _, port := range gatewayPorts {
				n, taken := holder[port]
				if !taken || n == ContainerGateway || n == ContainerGateway+"-test" {
					continue
				}
				got = n
				break
			}
			if got != c.wantHolder {
				t.Errorf("conflict holder = %q, want %q", got, c.wantHolder)
			}
		})
	}
}

// Docker's wording varies; missing it fails the install late, over-matching blocks a good install.
func TestIsPortTakenRecognizesDockersWordings(t *testing.T) {
	taken := []string{
		"driver failed programming external connectivity on endpoint x: Bind for 0.0.0.0:80 failed: port is already allocated",
		"listen tcp4 0.0.0.0:443: bind: address already in use",
		"Ports are not available: exposing port TCP 0.0.0.0:80 -> 127.0.0.1:0",
	}
	for _, m := range taken {
		if !isPortTaken(errors.New(m)) {
			t.Errorf("not recognized as a busy port: %q", m)
		}
	}
	notTaken := []string{
		"no such image: busybox:1.36",
		"Cannot connect to the Docker daemon",
	}
	for _, m := range notTaken {
		if isPortTaken(errors.New(m)) {
			t.Errorf("a broken probe was reported as a busy port: %q", m)
		}
	}
}

// A control plane crash-looping on a bad DB password still starts, and pg_isready never checks credentials.
func TestEveryComponentIsHealthGated(t *testing.T) {
	m := testManifest()
	s := &Service{}

	for _, c := range s.components(m) {
		t.Run(c.Name, func(t *testing.T) {
			if !c.WaitHealthy {
				t.Error("converge does not wait for this component to be healthy — a container " +
					"that starts and then dies would be reported as a successful install")
			}
			if c.Build(m, c.Name, *c.Image(m)).Healthcheck == nil {
				t.Error("no healthcheck: `miabi status` cannot report health for it, and the " +
					"converge gate falls back to guessing from uptime alone")
			}
		})
	}
}

// /readyz pings Postgres, so a blip would have Docker restart Miabi; probes run on loopback inside the container.
func TestHealthProbesHitHealthzOnLoopback(t *testing.T) {
	m := testManifest()

	for _, c := range []struct {
		name string
		spec docker.RunSpec
		want string
	}{
		{"control plane", controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi), "127.0.0.1:9000/healthz"},
		{"gateway", gatewaySpec(m, ContainerGateway, m.Images.Gateway), "127.0.0.1/healthz"},
	} {
		t.Run(c.name, func(t *testing.T) {
			probe := strings.Join(c.spec.Healthcheck.Test, " ")
			if !strings.Contains(probe, c.want) {
				t.Errorf("probe %q does not hit %s", probe, c.want)
			}
			if strings.Contains(probe, "/readyz") {
				t.Error("probing /readyz would restart the panel whenever a dependency blips")
			}
		})
	}
}

// A non-empty MIABI_REGISTRY_* pins the UI, so "off" must mean the keys are absent, not false.
func TestRegistryEnvIsAbsentUnlessEnabled(t *testing.T) {
	hasRegistryEnv := func(spec docker.RunSpec) bool {
		for _, e := range spec.Env {
			if strings.HasPrefix(e, "MIABI_REGISTRY_") {
				return true
			}
		}
		return false
	}

	t.Run("off: no keys at all", func(t *testing.T) {
		m := testManifest()
		spec := controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi)
		if hasRegistryEnv(spec) {
			t.Errorf("MIABI_REGISTRY_* was written while the registry is off — "+
				"that pins the setting and the UI could never enable it: %v", spec.Env)
		}
	})

	t.Run("on: enabled + host", func(t *testing.T) {
		m := testManifest()
		m.Registry.Enabled = true
		if err := m.Normalize(); err != nil {
			t.Fatal(err)
		}
		spec := controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi)

		want := map[string]bool{
			"MIABI_REGISTRY_ENABLED=true":                    false,
			"MIABI_REGISTRY_HOST=registry.miabi.example.com": false,
		}
		for _, e := range spec.Env {
			if _, ok := want[e]; ok {
				want[e] = true
			}
		}
		for e, found := range want {
			if !found {
				t.Errorf("missing %q; got %v", e, spec.Env)
			}
		}
	})
}

// Otherwise a converge on a running stack decides "up to date" and never enables it.
func TestEnablingTheRegistryRecreatesTheControlPlane(t *testing.T) {
	off := testManifest()
	on := *off
	on.Registry.Enabled = true
	if err := on.Normalize(); err != nil {
		t.Fatal(err)
	}
	if specHash(controlPlaneSpec(off, ContainerControlPlane, off.Images.Miabi)) ==
		specHash(controlPlaneSpec(&on, ContainerControlPlane, on.Images.Miabi)) {
		t.Error("enabling the registry did not change the spec hash — `miabi install --registry` " +
			"on a running stack would report 'up to date' and do nothing")
	}
}

// A bad host makes the gateway request a certificate for an impossible name, burning ACME rate limit.
func TestRegistryHostIsValidated(t *testing.T) {
	cases := []struct {
		host    string
		wantErr bool
		wantOut string
	}{
		{"", false, "registry.miabi.example.com"}, // derived from the domain
		{"registry.example.com", false, "registry.example.com"},
		{"REGISTRY.Example.COM", false, "registry.example.com"}, // hostnames are case-insensitive
		{"y", true, ""},                     // the classic: a stray prompt answer
		{"yes", true, ""},                   // no dot: not a hostname
		{"registry_.example.com", true, ""}, // underscore
		{"-registry.example.com", true, ""}, // leading hyphen
		{"registry..example.com", true, ""}, // empty label
		{"miabi.example.com", true, ""},     // the panel's own name
	}
	for _, c := range cases {
		name := c.host
		if name == "" {
			name = "(empty → derived)"
		}
		t.Run(name, func(t *testing.T) {
			m := testManifest()
			m.Registry.Enabled = true
			m.Registry.Host = c.host

			err := m.Normalize()
			if c.wantErr {
				if err == nil {
					t.Errorf("accepted %q as a registry hostname — the gateway would request a "+
						"certificate for it", c.host)
				}
				return
			}
			if err != nil {
				t.Fatalf("rejected a valid host: %v", err)
			}
			if m.Registry.Host != c.wantOut {
				t.Errorf("host = %q, want %q", m.Registry.Host, c.wantOut)
			}
		})
	}
}

// env: is where an operator adds anything Miabi reads that the manifest does not model.
func TestExtraEnvIsInjectedIntoTheControlPlane(t *testing.T) {
	m := testManifest()
	m.Env = map[string]string{
		"MIABI_SMTP_HOST": "smtp.example.com",
		"MIABI_LOG_LEVEL": "debug",
		"TZ":              "Europe/Paris",
	}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, kv := range controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi).Env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for k, want := range m.Env {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	// The managed ones must survive alongside.
	if got["MIABI_DB_PASSWORD"] != m.Secrets.DBPassword {
		t.Error("extra env displaced a managed variable")
	}
}

// A duplicate env key resolves by ordering; an operator override could hand the control plane the wrong DB password.
func TestManagedEnvCannotBeOverridden(t *testing.T) {
	// TZ and MIABI_LOG_LEVEL are seeded defaults, not managed, so they must stay editable.
	base := testManifest()
	base.Env = nil
	spec := controlPlaneSpec(base, ContainerControlPlane, base.Images.Miabi)

	var managed []string
	for _, kv := range spec.Env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			managed = append(managed, k)
		}
	}
	if len(managed) < 10 {
		t.Fatalf("expected the control plane to set many variables, got %d", len(managed))
	}

	for _, key := range managed {
		t.Run(key, func(t *testing.T) {
			m := testManifest()
			m.Env = map[string]string{key: "hijacked"}
			err := m.Normalize()
			if err == nil {
				t.Fatalf("env: %s was accepted — it would collide with the value Miabi sets, "+
					"and the winner is decided by an ordering rule, not by anyone's intent", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("the error does not name the offending key: %v", err)
			}
		})
	}
}

// Two sources of truth would let status and the plan say "off" while the container had it on.
func TestRegistryCannotBeSetThroughEnv(t *testing.T) {
	m := testManifest()
	m.Env = map[string]string{"MIABI_REGISTRY_ENABLED": "true"}
	err := m.Normalize()
	if err == nil {
		t.Fatal("MIABI_REGISTRY_ENABLED was accepted through env: — the plan and status would " +
			"report the registry as off while it was actually on")
	}
	if !strings.Contains(err.Error(), "registry") {
		t.Errorf("the error does not point at the registry section: %v", err)
	}
}

// Only the registry settings the manifest models are reserved; the rest are ordinary env entries.
func TestUnmodelledRegistrySettingsGoThroughEnv(t *testing.T) {
	for _, key := range []string{"MIABI_REGISTRY_STORAGE_CLASS", "MIABI_REGISTRY_S3_BUCKET", "MIABI_REGISTRY_HTTPS_REDIRECT"} {
		m := testManifest()
		m.Env = map[string]string{key: "x"}
		if err := m.Normalize(); err != nil {
			t.Errorf("%s refused through env: %v", key, err)
		}
	}
}

// Set on one side only, Goma reads a config it cannot decrypt; its home is gateway.env.
func TestGomaEncryptionKeyReachesBothMiabiAndTheGateway(t *testing.T) {
	m := testManifest()
	m.Gateway.Env = map[string]string{gomaConfigEncryptionKey: "shared-secret"}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}

	has := func(spec docker.RunSpec) bool {
		for _, kv := range spec.Env {
			if kv == gomaConfigEncryptionKey+"=shared-secret" {
				return true
			}
		}
		return false
	}
	if !has(controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi)) {
		t.Error("the control plane did not get the key — it would write unencrypted config")
	}
	if !has(gatewaySpec(m, ContainerGateway, m.Images.Gateway)) {
		t.Error("the gateway did not get the key — it would read config it cannot decrypt, " +
			"and routing would silently break")
	}
}

func TestExtraEnvNamesAreValidated(t *testing.T) {
	for _, bad := range []string{"has space", "1LEADING_DIGIT", "has-hyphen", "has=equals", ""} {
		m := testManifest()
		m.Env = map[string]string{bad: "x"}
		if err := m.Normalize(); err == nil {
			t.Errorf("accepted %q as an environment variable name", bad)
		}
	}
	for _, good := range []string{"MIABI_SMTP_HOST", "TZ", "_UNDERSCORE", "A1"} {
		m := testManifest()
		m.Env = map[string]string{good: "x"}
		if err := m.Normalize(); err != nil {
			t.Errorf("rejected the valid name %q: %v", good, err)
		}
	}
}

// Otherwise an edited env would be reported "up to date" and never applied.
func TestChangingExtraEnvRecreatesTheControlPlane(t *testing.T) {
	before := testManifest()
	after := *before
	after.Env = map[string]string{"MIABI_LOG_LEVEL": "debug"}
	if err := after.Normalize(); err != nil {
		t.Fatal(err)
	}
	if specHash(controlPlaneSpec(before, ContainerControlPlane, before.Images.Miabi)) ==
		specHash(controlPlaneSpec(&after, ContainerControlPlane, after.Images.Miabi)) {
		t.Error("adding an env var did not change the spec hash — `miabi install` would say " +
			"'up to date' and the setting would never take effect")
	}
}

// Seeded into the manifest so operators can see the knobs they most likely want.
func TestEnvDefaultsAreSeededIntoTheManifest(t *testing.T) {
	m := Defaults("miabi/miabi:1.4.0")
	m.Domain = "miabi.example.com"
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if got := m.Env[envTimezone]; got != "UTC" {
		t.Errorf("TZ = %q, want UTC", got)
	}
	if got := m.Env[envLogLevel]; got != "info" {
		t.Errorf("MIABI_LOG_LEVEL = %q, want info", got)
	}
}

// Seeding that overwrote a hand edit would silently revert it on the next install.
func TestSeededDefaultsNeverOverwriteTheOperator(t *testing.T) {
	m := testManifest()
	m.Env = map[string]string{envTimezone: "Europe/Paris", envLogLevel: "debug"}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if m.Env[envTimezone] != "Europe/Paris" || m.Env[envLogLevel] != "debug" {
		t.Fatalf("seeding clobbered the operator's values: %v", m.Env)
	}
}

// Applied to the control plane alone, Miabi's logs and its containers' logs would sit in different timezones.
func TestTimezoneReachesEveryComponent(t *testing.T) {
	m := testManifest()
	m.Env = map[string]string{envTimezone: "Europe/Paris"}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	s := &Service{}

	for _, c := range s.components(m) {
		t.Run(c.Name, func(t *testing.T) {
			spec := c.Build(m, c.Name, *c.Image(m))
			for _, kv := range spec.Env {
				if kv == "TZ=Europe/Paris" {
					return
				}
			}
			t.Errorf("%s did not get TZ — its logs would be timestamped in a different "+
				"timezone from the rest of the stack: %v", c.Name, spec.Env)
		})
	}
}

// Left to the control plane, a bad level crash-loops and fails as "did not become healthy" much later.
func TestBadLogLevelIsRejectedByTheInstallerNotTheContainer(t *testing.T) {
	m := testManifest()
	m.Env = map[string]string{envLogLevel: "verbose"}
	err := m.Normalize()
	if err == nil {
		t.Fatal("accepted MIABI_LOG_LEVEL=verbose — the control plane would crash-loop and " +
			"the install would fail on a health timeout instead of naming the mistake")
	}
	if !strings.Contains(err.Error(), "verbose") {
		t.Errorf("the error does not name the bad value: %v", err)
	}
}

// Some hosts refuse the bind; turning it off is safe since /proc in a container already reflects the host.
func TestHostProcBindCanBeDisabled(t *testing.T) {
	hasProcBind := func(spec docker.RunSpec) bool {
		for _, b := range spec.Binds {
			if b.Source == "/proc" {
				return true
			}
		}
		return false
	}
	hasSocket := func(spec docker.RunSpec) bool {
		for _, b := range spec.Binds {
			if b.Source == "/var/run/docker.sock" {
				return true
			}
		}
		return false
	}

	t.Run("on by default", func(t *testing.T) {
		m := testManifest()
		if !hasProcBind(controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi)) {
			t.Error("the /proc bind is missing by default — the Nodes page loses real host metrics")
		}
	})

	t.Run("off when disabled", func(t *testing.T) {
		m := testManifest()
		off := false
		m.HostProc = &off
		spec := controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi)

		if hasProcBind(spec) {
			t.Error("/proc was still bound after being disabled — the install would fail on a " +
				"host that refuses the bind, which is the only reason to set this")
		}
		// The socket is not optional: without it Miabi manages nothing at all.
		if !hasSocket(spec) {
			t.Error("disabling the /proc bind also dropped the Docker socket")
		}
	})
}

// A pointer so manifests predating the field keep the bind; Normalize writes the resolved value back.
func TestAbsentHostProcMeansOnAndIsWrittenBack(t *testing.T) {
	m := Defaults("miabi/miabi:1.4.0")
	m.Domain = "miabi.example.com"
	m.HostProc = nil // a manifest from before the field existed

	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if m.HostProc == nil {
		t.Fatal("Normalize left host_proc unresolved — the manifest would not say what it does")
	}
	if !*m.HostProc {
		t.Error("an older manifest silently lost its /proc bind on converge")
	}
}

// Otherwise the container keeps a bind the host refuses while setup reports "up to date".
func TestTogglingHostProcRecreatesTheControlPlane(t *testing.T) {
	on := testManifest()
	off := *on
	no := false
	off.HostProc = &no

	if specHash(controlPlaneSpec(on, ContainerControlPlane, on.Images.Miabi)) ==
		specHash(controlPlaneSpec(&off, ContainerControlPlane, off.Images.Miabi)) {
		t.Error("disabling the /proc bind did not change the spec hash — converge would " +
			"report 'up to date' and leave the old container in place")
	}
}

// New credentials against an existing Postgres volume lock the control plane out of its own database.
func TestReinstallKeepsExistingSecrets(t *testing.T) {
	m := testManifest()
	before := m.Secrets

	if err := m.Normalize(); err != nil { // as `miabi install` re-runs it
		t.Fatal(err)
	}
	if m.Secrets != before {
		t.Fatalf("Normalize rotated the secrets of an existing install:\n before %+v\n after  %+v", before, m.Secrets)
	}
}

func TestGenerateSecretsFillsOnlyWhatIsMissing(t *testing.T) {
	m := &Manifest{Secrets: Secrets{DBPassword: "keep-me"}}
	if err := m.GenerateSecrets(); err != nil {
		t.Fatal(err)
	}
	if m.Secrets.DBPassword != "keep-me" {
		t.Error("an existing secret was overwritten")
	}
	for name, v := range map[string]string{
		"redis":      m.Secrets.RedisPassword,
		"jwt":        m.Secrets.JWTSecret,
		"encryption": m.Secrets.EncryptionKey,
		"admin":      m.Secrets.AdminPassword,
	} {
		if v == "" {
			t.Errorf("%s secret was left empty — the control plane would refuse to boot in production", name)
		}
	}
}

// The manifest is the only copy of the database password, so it is written atomically at 0600.
func TestSaveIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "stack.yaml")

	m := testManifest()
	if err := Save(path, m); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode %o, want 600 — the file holds the database password in plain text", perm)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("the temp file was left behind")
	}

	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Secrets != m.Secrets || back.Domain != m.Domain {
		t.Error("the manifest did not round-trip")
	}
}

func TestLoadDistinguishesMissingFromCorrupt(t *testing.T) {
	dir := t.TempDir()

	// A Compose user sees this and must not be told their install is broken.
	if _, err := Load(filepath.Join(dir, "absent.yaml")); err == nil || !strings.Contains(err.Error(), "no Miabi stack manifest") {
		t.Errorf("missing manifest gave %v, want ErrNotInstalled", err)
	}

	// Present but not ours.
	junk := filepath.Join(dir, "junk.yaml")
	if err := os.WriteFile(junk, []byte("hello: world\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(junk); err == nil || !strings.Contains(err.Error(), "not a Miabi stack manifest") {
		t.Errorf("a versionless file gave %v, want a 'not a manifest' error", err)
	}

	// From a newer CLI: refuse rather than misread it.
	future := filepath.Join(dir, "future.yaml")
	if err := os.WriteFile(future, []byte("version: 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(future); err == nil || !strings.Contains(err.Error(), "upgrade the CLI") {
		t.Errorf("a future manifest gave %v, want a refusal", err)
	}
}

func TestNormalizeRequiresADomainAndDerivesTheRest(t *testing.T) {
	m := Defaults("miabi/miabi:1.4.0")
	if err := m.Normalize(); err == nil {
		t.Error("no domain was accepted — the gateway would have no hostname to serve")
	}

	m.Domain = "panel.example.com"
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if m.WebURL != "https://panel.example.com" {
		t.Errorf("WebURL = %q", m.WebURL)
	}
	if m.ACMEEmail != "admin@panel.example.com" {
		t.Errorf("ACMEEmail = %q", m.ACMEEmail)
	}
}

// A private-network node may reach the control plane at an address the public URL never resolves to.
func TestControlURLDefaultsToTheWebURL(t *testing.T) {
	m := Defaults("miabi/miabi:1.4.0")
	m.Domain = "miabi.example.com"
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if m.ControlURL != m.WebURL {
		t.Errorf("control_url = %q, want the web URL %q", m.ControlURL, m.WebURL)
	}
	if !envHas(controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi), "MIABI_CONTROL_URL="+m.WebURL) {
		t.Error("MIABI_CONTROL_URL did not reach the control plane")
	}
}

func TestControlURLCanDifferFromTheWebURL(t *testing.T) {
	m := testManifest()
	m.ControlURL = "https://miabi.internal:9000/" // trailing slash: the agent trims it, so we must too
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if m.ControlURL != "https://miabi.internal:9000" {
		t.Errorf("control_url = %q — the trailing slash was not trimmed, so the manifest "+
			"says one thing and the agent another", m.ControlURL)
	}
	spec := controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi)
	if !envHas(spec, "MIABI_CONTROL_URL=https://miabi.internal:9000") {
		t.Error("the custom control URL did not reach the control plane")
	}
	// The panel's own URL is untouched — they are separate settings.
	if !envHas(spec, "MIABI_WEB_URL="+m.WebURL) {
		t.Error("setting control_url changed the panel's own URL")
	}
}

// A bad control URL only surfaces later when an agent can't connect or a node gateway fetches no routes.
func TestBadControlURLIsRejectedAtInstall(t *testing.T) {
	for _, bad := range []string{"miabi.example.com", "ftp://x.example.com", "not a url", "://nope"} {
		m := testManifest()
		m.ControlURL = bad
		if err := m.Normalize(); err == nil {
			t.Errorf("accepted control_url=%q — agents would be handed something they cannot dial", bad)
		}
	}
}

// It is a manifest field now, so env: must not be a second way to set it.
func TestControlURLCannotBeSetThroughEnv(t *testing.T) {
	m := testManifest()
	m.Env = map[string]string{"MIABI_CONTROL_URL": "https://elsewhere.example.com"}
	if err := m.Normalize(); err == nil {
		t.Error("MIABI_CONTROL_URL was accepted through env: — it would collide with the value " +
			"Miabi derives from control_url, and the winner is decided by an ordering rule")
	}
}

func envHas(spec docker.RunSpec, kv string) bool {
	for _, e := range spec.Env {
		if e == kv {
			return true
		}
	}
	return false
}

// In the top-level env only the control plane would get the key, so Goma couldn't decrypt the config.
func TestGomaEncryptionKeyIsRefusedInTheTopLevelEnv(t *testing.T) {
	m := testManifest()
	m.Env = map[string]string{gomaConfigEncryptionKey: "shared-secret"}
	err := m.Normalize()
	if err == nil {
		t.Fatal("accepted GOMA_CONFIG_ENCRYPTION_KEY in env: — the gateway would never receive it")
	}
	if !strings.Contains(err.Error(), "gateway.env") {
		t.Errorf("the error does not point at its real home: %v", err)
	}
}

// The reserved set is derived from gatewaySpec so it cannot drift.
func TestGatewayEnvCannotOverrideWhatMiabiSets(t *testing.T) {
	for _, key := range []string{"MIABI_DOMAIN", "MIABI_ACME_EMAIL", "MIABI_REDIS_PASSWORD"} {
		m := testManifest()
		m.Gateway.Env = map[string]string{key: "hijacked"}
		if err := m.Normalize(); err == nil {
			t.Errorf("gateway.env: %s was accepted — it would collide with the value Miabi "+
				"derives from the manifest", key)
		}
	}
}

func TestGatewayEnvReachesTheGateway(t *testing.T) {
	m := testManifest()
	m.Gateway.Env = map[string]string{"MY_UPSTREAM": "https://internal.example.com"}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	spec := gatewaySpec(m, ContainerGateway, m.Images.Gateway)
	if !envHas(spec, "MY_UPSTREAM=https://internal.example.com") {
		t.Errorf("a custom gateway variable never reached Goma — its config could not "+
			"interpolate ${MY_UPSTREAM}: %v", spec.Env)
	}
}

// A volume would make itself the source of truth, so the operator could never customize the config.
func TestGatewayConfigIsBoundFromTheHostNotAVolume(t *testing.T) {
	m := testManifest()
	m.gatewayHostConfig = "/etc/miabi/goma.yml"
	spec := gatewaySpec(m, ContainerGateway, m.Images.Gateway)

	bound := false
	for _, b := range spec.Binds {
		if b.Source == "/etc/miabi/goma.yml" && b.Target == gomaConfigFile {
			if !b.ReadOnly {
				t.Error("the gateway config is writable — Goma could rewrite the operator's file")
			}
			bound = true
		}
	}
	if !bound {
		t.Errorf("goma.yml is not bind-mounted from the host: %+v", spec.Binds)
	}
	if _, ok := spec.Mounts[VolumeGatewayConfig]; ok {
		t.Error("the config volume is still mounted — it would shadow the bind and " +
			"resurrect the 'every converge overwrites your edits' behaviour")
	}
}

// Otherwise a new config path would be reported "up to date" while the old file keeps running.
func TestChangingTheGatewayConfigRecreatesTheGateway(t *testing.T) {
	a := testManifest()
	a.gatewayHostConfig = "/etc/miabi/goma.yml"
	b := *a
	b.gatewayHostConfig = "/etc/miabi/custom.yml"

	if specHash(gatewaySpec(a, ContainerGateway, a.Images.Gateway)) ==
		specHash(gatewaySpec(&b, ContainerGateway, b.Images.Gateway)) {
		t.Error("pointing at a different config did not change the spec hash")
	}
}

func TestGomaLogLevelIsSeeded(t *testing.T) {
	m := testManifest()
	if got := m.Gateway.Env[envGomaLogLevel]; got != "info" {
		t.Errorf("GOMA_LOG_LEVEL = %q, want info", got)
	}
	if !envHas(gatewaySpec(m, ContainerGateway, m.Images.Gateway), "GOMA_LOG_LEVEL=info") {
		t.Error("the seeded level never reached Goma")
	}
}

func TestGomaLogLevelSeedNeverOverwritesTheOperator(t *testing.T) {
	m := testManifest()
	m.Gateway.Env = map[string]string{envGomaLogLevel: "debug"}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if m.Gateway.Env[envGomaLogLevel] != "debug" {
		t.Error("seeding clobbered the operator's value")
	}
}

// The analytics consumer runs by default, so without the stream every dashboard is silently empty.
func TestGomaAnalyticsIsEnabledByDefault(t *testing.T) {
	m := testManifest()
	if got := m.Gateway.Env[envGomaAnalytics]; got != "true" {
		t.Errorf("GOMA_ANALYTICS_ENABLED = %q, want true", got)
	}
	if !envHas(gatewaySpec(m, ContainerGateway, m.Images.Gateway), "GOMA_ANALYTICS_ENABLED=true") {
		t.Error("the seeded value never reached Goma")
	}
}

// Seeded into gateway.env, not gatewaySpec, so operators can still switch analytics off.
func TestGomaAnalyticsCanBeTurnedOff(t *testing.T) {
	m := testManifest()
	m.Gateway.Env = map[string]string{envGomaAnalytics: "false"}
	if err := m.Normalize(); err != nil {
		t.Fatalf("operator could not disable analytics: %v", err)
	}
	if got := m.Gateway.Env[envGomaAnalytics]; got != "false" {
		t.Errorf("GOMA_ANALYTICS_ENABLED = %q, want the operator's false", got)
	}
	if !envHas(gatewaySpec(m, ContainerGateway, m.Images.Gateway), "GOMA_ANALYTICS_ENABLED=false") {
		t.Error("the operator's value never reached Goma")
	}
}

// Goma silently falls back to info on an unknown level, so a typo must be caught at install.
func TestBadGomaLogLevelIsRejected(t *testing.T) {
	m := testManifest()
	m.Gateway.Env = map[string]string{envGomaLogLevel: "verbose"}
	err := m.Normalize()
	if err == nil {
		t.Fatal("accepted GOMA_LOG_LEVEL=verbose — Goma would silently run at info and the " +
			"operator would never know why their debug logs are missing")
	}
	if !strings.Contains(err.Error(), "verbose") {
		t.Errorf("the error does not name the bad value: %v", err)
	}
}

// The gateway spec already sets TZ, so seeding it would be refused as a managed key.
func TestTimezoneIsNotSeededIntoGatewayEnv(t *testing.T) {
	m := testManifest()
	if _, ok := m.Gateway.Env[envTimezone]; ok {
		t.Error("TZ was seeded into gateway.env — it is already applied stack-wide, and " +
			"normalizeGateway refuses it as a managed key, so every install would fail")
	}
	// …and the gateway still gets it.
	if !envHas(gatewaySpec(m, ContainerGateway, m.Images.Gateway), "TZ=UTC") {
		t.Error("the gateway lost TZ")
	}
}

// Restart must walk components in dependency order, or the control plane returns before its database.
func TestRestartWalksComponentsInDependencyOrder(t *testing.T) {
	m := testManifest()
	s := &Service{}

	var order []string
	for _, c := range s.components(m) {
		order = append(order, c.Name)
	}
	want := []string{ContainerPostgres, ContainerRedis, ContainerControlPlane, ContainerGateway}
	if len(order) != len(want) {
		t.Fatalf("components = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("restart order = %v, want %v — the control plane must not come back "+
				"before the database it exits without", order, want)
		}
	}
}

// Either email fills in for the other; admin@<domain> is invented only when neither is given.
func TestAcmeAndAdminEmailFallBackToEachOther(t *testing.T) {
	cases := []struct {
		name, acme, admin   string
		wantAcme, wantAdmin string
	}{
		{
			"only acme given → it becomes the admin login",
			"ops@example.com", "",
			"ops@example.com", "ops@example.com",
		},
		{
			"only admin given → it becomes the ACME contact",
			"", "boss@example.com",
			"boss@example.com", "boss@example.com",
		},
		{
			"both given → both kept",
			"ops@example.com", "boss@example.com",
			"ops@example.com", "boss@example.com",
		},
		{
			"neither given → derived from the domain (the only guess)",
			"", "",
			"admin@miabi.example.com", "admin@miabi.example.com",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Defaults("miabi/miabi:1.4.0")
			m.Domain = "miabi.example.com"
			m.ACMEEmail, m.Secrets.AdminEmail = c.acme, c.admin

			if err := m.Normalize(); err != nil {
				t.Fatal(err)
			}
			if m.ACMEEmail != c.wantAcme {
				t.Errorf("acme_email = %q, want %q", m.ACMEEmail, c.wantAcme)
			}
			if m.Secrets.AdminEmail != c.wantAdmin {
				t.Errorf("admin_email = %q, want %q", m.Secrets.AdminEmail, c.wantAdmin)
			}

			// Both must actually reach the containers that need them.
			spec := controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi)
			if !envHas(spec, "MIABI_ADMIN_EMAIL="+c.wantAdmin) {
				t.Error("the admin login never reached the control plane")
			}
			if !envHas(gatewaySpec(m, ContainerGateway, m.Images.Gateway), "MIABI_ACME_EMAIL="+c.wantAcme) {
				t.Error("the ACME contact never reached the gateway")
			}
		})
	}
}

// A typo would only surface later as an unrelated-looking ACME error.
func TestBadEmailsAreRejected(t *testing.T) {
	for _, bad := range []string{"not-an-email", "@example.com", "ops@localhost", "a@b@c.com", "ops @example.com"} {
		m := Defaults("miabi/miabi:1.4.0")
		m.Domain = "miabi.example.com"
		m.ACMEEmail = bad
		if err := m.Normalize(); err == nil {
			t.Errorf("accepted %q as an email address", bad)
		}
	}
}
