// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformstack

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// The CLI seeds this file into the gateway's config volume; the Compose path
// bind-mounts deploy/goma/goma.yml. They must be the same file, or the two install
// paths route differently — and the one nobody is running is the one that rots.
func TestEmbeddedGomaConfigMatchesTheComposeOne(t *testing.T) {
	onDisk, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "goma", "goma.yml"))
	if err != nil {
		t.Fatalf("read deploy/goma/goma.yml: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(onDisk), bytes.TrimSpace(GomaConfig())) {
		t.Error("assets/goma.yml has drifted from deploy/goma/goma.yml — " +
			"the CLI install and the Compose install would serve different routing rules. " +
			"Copy deploy/goma/goma.yml over internal/services/platformstack/assets/goma.yml.")
	}
}

// Git tags carry a leading "v"; Docker tags do not. Pinning the git form yields an
// image reference that does not exist — and the install discovers that only at the
// pull, after the database is already up.
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

// Converge decides "already correct" vs "changed" by hashing the run spec. If the
// hash were unstable, every `miabi install` would recreate the whole stack — which
// for Postgres means restarting the database for no reason. Go map iteration is
// random, so this is a live hazard, not a theoretical one.
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

	// Copy m and change ONE field. testManifest() would generate fresh random secrets,
	// so two independent manifests differ for reasons that have nothing to do with the
	// field under test — and the assertion would pass no matter what specHash covered.
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

// The rollout starts the new gateway image under a second name while the live one is
// still up. Two containers cannot both bind :443, so the test container must not
// publish ports — otherwise every gateway update fails at the first step.
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

// Every component must be recognizable as Miabi's own: Phase 1's guards (import,
// destructive ops) read these labels, and a component that skipped them would be
// offered for import and be stoppable from the containers page.
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

// Being part of Miabi is not the same as being part of the STACK. The built-in
// registry, the node agents and every remote node's edge gateway all carry
// part-of=miabi — they are platform infrastructure Miabi provisions on demand, which
// the CLI neither installs nor updates. Listing them in `miabi status` implies it
// does. (Caught by running `miabi status` on a real host, which reported mb-registry.)
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

// The gateway comes up LAST, so a port clash discovered at bind time leaves Postgres,
// Redis and the control plane already running — a half-built stack, and an error about
// a port at the worst possible moment. The check runs before anything is created.
//
// The subtle half is the reinstall: our OWN gateway is holding :80 and :443, and
// treating that as a conflict would make every converge on a healthy stack fail.
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
			// The most likely real conflict: they already run Miabi under Compose, whose
			// gateway is project-prefixed and so is NOT ContainerGateway.
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
			// Reproduce CheckPorts' container pass. (The bind probe needs a live engine and
			// is exercised end-to-end, not here.)
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

// Docker's wording for a taken port varies by platform and version. Getting this wrong
// in either direction is bad: miss it and the install dies late; over-match and a
// broken probe blocks a perfectly good install.
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

// Converge must not call a component "up" merely because `docker run` returned. A
// control plane crash-looping on a bad database password still starts — and without a
// health gate the install printed "✓ Miabi is up" while the panel was down. Observed,
// not hypothetical: an existing pgdata volume plus a fresh manifest produces exactly
// that, and pg_isready is happy throughout because it never checks credentials.
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

// The probes must hit the paths the services actually serve. /healthz is right for
// BOTH (verified against a live Goma: /healthz and /readyz answer 200, /health is a
// 404) — and both are probed on the loopback INSIDE the container, so a rollout's test
// container, which publishes no ports, is checked exactly like the live one.
//
// /readyz would be the wrong choice for Miabi: it pings Postgres, Redis and Docker, so
// a database blip would mark the panel unhealthy and Docker would restart it — which
// cannot fix a database, and only adds an outage to an outage.
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

// A non-empty MIABI_REGISTRY_* value is a ONE-WAY OVERRIDE: it pins the setting and
// the admin UI can no longer change it. So "registry off" must mean the keys are
// ABSENT, not present-and-false — the latter would lock the UI out of ever enabling
// it, which is the opposite of what an operator who never mentioned the registry
// wants.
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

// Enabling the registry must also change the control plane's spec hash, or a converge
// on an already-running stack would decide "up to date" and never actually turn it on.
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

// The registry host gets a public DNS record and its OWN certificate, so a nonsense
// value makes the gateway ask Let's Encrypt for a name that cannot exist — burning
// rate limit and failing far from the mistake. (install.sh guards this because a
// stray "y" from the preceding y/N prompt was how it actually happened.)
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

// The heart of it: a variable Miabi sets itself must be REFUSED, not merged. A
// duplicate key in a container's environment resolves by an ordering rule nobody
// should have to reason about — and if the operator's value won, the control plane
// would get a database password that does not open its database.
//
// The reserved set is DERIVED from the control plane's spec, so this test asserts
// against what the spec actually emits, not a list that could drift from it.
func TestManagedEnvCannotBeOverridden(t *testing.T) {
	// Derive the managed set the way normalizeEnv does: from a spec built with NO user
	// env. Reading it off a spec that already carries the operator's entries would
	// count TZ and MIABI_LOG_LEVEL as "managed" — they are not; they are ordinary env:
	// keys that Miabi merely seeds a default for, and they must stay editable.
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

// The registry has its own manifest section and its own flag. Letting env: set it too
// would give two sources of truth: `miabi status` and the install plan would both say
// "off" while the container had it on.
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

// GOMA_CONFIG_ENCRYPTION_KEY must hold the same value in TWO containers: Miabi
// encrypts the gateway config, Goma decrypts it. Set on one side only, Goma reads a
// config it cannot decrypt and routing breaks with no obvious cause.
//
// Its home is gateway.env, and Miabi forwards it to the control plane from there.
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

// Changing env: must recreate the control plane, or an operator would edit the
// manifest, re-run install, be told "up to date", and never get their setting.
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

// TZ and MIABI_LOG_LEVEL are seeded INTO the manifest rather than applied invisibly,
// so an operator opening stack.yaml can see the two knobs they are most likely to want
// and where to change them.
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

// …but they are ordinary env: entries, so an operator's value always wins. Seeding
// that overwrote a hand-edited manifest would silently revert their setting on the
// next `miabi install`.
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

// TZ belongs to the WHOLE stack. Applied to the control plane alone, Miabi's logs sit
// in one timezone and the logs of every container it manages sit in another — exactly
// the situation you least want when reading a timeline across them.
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

// A bad log level must be caught HERE. Left to the control plane, it is rejected at
// startup — so the container crash-loops and the install fails two minutes later with
// "did not become healthy", nowhere near the actual cause.
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

// The /proc bind is optional: some hosts refuse it (a rootless daemon, a hardened
// host, a socket proxy that forbids host binds). Turning it off is safe — Miabi falls
// back to its own /proc, which inside a container already reflects host CPU/memory.
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

// Absent must mean ON. A plain bool's zero value is false, so every manifest written
// before this field existed would have silently lost the bind on the next converge —
// which is why the field is a pointer. Normalize resolves nil and writes it back, so
// the file always states what it does.
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

// Toggling it must recreate the control plane, or an operator would set the flag, be
// told "up to date", and still have a container with the bind that their host refuses.
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

// A second install must not rotate the secrets of a live stack: new credentials
// against an existing Postgres volume means the control plane cannot log in to its
// own database, and the data is effectively lost.
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

// The manifest is the only copy of the database password. A partial write loses it,
// and with it the database — so it is written atomically, at 0600.
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

	// Missing: "you have not installed" — a Compose user sees this and must not be
	// told their install is broken.
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

// ControlURL is where remote nodes and agents dial back. It defaults to the panel's
// public URL — right for a single hostname — but must not be WELDED to it: a node on
// a private network may reach the control plane at an address the public URL never
// resolves to, and pinning the two together would force that traffic out over the
// internet and back.
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

// A bad control URL fails far from the mistake: the panel works, and only later does
// an agent refuse to connect or a node's gateway quietly fetch no routes.
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

// Setting the shared key in the TOP-LEVEL env: would give it to the control plane
// only — Miabi would encrypt a config Goma cannot decrypt, and routing would break
// with no obvious cause. It has exactly one home, and the error says which.
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

// gateway.env is for what the config interpolates. It must not be a second way to set
// what Miabi already derives from the manifest — same rule as the top-level env:, and
// the reserved set is derived from gatewaySpec so it cannot drift from it.
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

// The config is BIND-MOUNTED from the host, not copied into a volume. A volume would
// make itself the source of truth and the host file a stale duplicate that every
// converge overwrites — so the operator could never customize it at all.
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

// Changing the config path recreates the gateway; otherwise an operator would point at
// a new file, be told "up to date", and keep running the old one.
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

// GOMA_LOG_LEVEL is seeded into gateway.env so the knob is visible in the manifest
// rather than folklore.
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

// Goma does NOT reject an unknown level — it silently falls back to info (measured:
// GOMA_LOG_LEVEL=verbose emits the same 28 boot lines as info). So a typo leaves the
// operator waiting for debug output that was never coming. Catch it at install.
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

// TZ is stack-wide. It reaches the gateway from the top-level env:, so gateway.env
// must NOT seed it — two entries for one value, and the gateway's own spec already
// sets it, so it would be refused as a managed key on the very first install.
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
