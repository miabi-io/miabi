// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformstack

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

// gomaConfig is the central gateway's configuration. Goma expands ${...} from its
// environment at runtime, so the file is seeded verbatim and the domain / ACME email
// / Redis password are passed as env — meaning the config holds no secret and the
// panel's hostname lives in exactly one place (the manifest).
//
// It is byte-identical to deploy/goma/goma.yml, which the Compose path bind-mounts;
// a test asserts that, so the two install paths cannot drift into serving different
// routing rules.
//
//go:embed assets/goma.yml
var gomaConfig []byte

// GomaConfig exposes the embedded config so the drift test can compare it.
func GomaConfig() []byte { return gomaConfig }

const (
	gomaConfigDir  = "/etc/goma"
	gomaConfigFile = gomaConfigDir + "/goma.yml"
	gomaProviders  = gomaConfigDir + "/providers"
)

// postgresSpec — the platform database.
//
// No published ports: nothing outside the `miabi` network has any business reaching
// it, and publishing 5432 on a VPS is how self-hosted databases end up in breach
// reports. The Compose file does the same.
func postgresSpec(m *Manifest, name, image string) docker.RunSpec {
	spec := docker.RunSpec{
		Name:  name,
		Image: image,
		Env: []string{
			"POSTGRES_USER=miabi",
			"POSTGRES_PASSWORD=" + m.Secrets.DBPassword,
			"POSTGRES_DB=miabi",
		},
		Mounts:        map[string]string{VolumePGData: "/var/lib/postgresql/data"},
		Networks:      []string{m.Network.Name},
		RestartPolicy: "unless-stopped",
		Healthcheck: &docker.HealthcheckSpec{
			Test:     []string{"CMD-SHELL", "pg_isready -U miabi"},
			Interval: 5 * time.Second,
			Timeout:  3 * time.Second,
			Retries:  10,
		},
		Labels: docker.PlatformLabels(docker.RolePlatformDB, docker.ManagedByMiabi, nil),
	}
	applyTimezone(m, &spec)
	return spec
}

// redisSpec — cache, rate limiting, and the asynq queue. Shared with the gateway,
// which uses it for distributed rate limiting, so the password is in both.
func redisSpec(m *Manifest, name, image string) docker.RunSpec {
	spec := docker.RunSpec{
		Name:          name,
		Image:         image,
		Cmd:           []string{"redis-server", "--requirepass", m.Secrets.RedisPassword},
		Mounts:        map[string]string{VolumeRedisData: "/data"},
		Networks:      []string{m.Network.Name},
		RestartPolicy: "unless-stopped",
		Healthcheck: &docker.HealthcheckSpec{
			// -a, not a bare ping: with requirepass set, an unauthenticated PING answers
			// NOAUTH, and the container would sit "unhealthy" forever while working fine.
			Test:     []string{"CMD", "redis-cli", "-a", m.Secrets.RedisPassword, "ping"},
			Interval: 5 * time.Second,
			Timeout:  3 * time.Second,
			Retries:  10,
		},
		Labels: docker.PlatformLabels(docker.RolePlatformCache, docker.ManagedByMiabi, nil),
	}
	applyTimezone(m, &spec)
	return spec
}

// controlPlaneSpec — Miabi itself: API + web UI + the embedded worker.
//
// It mounts the Docker socket (that is the whole job) and shares the gateway's
// providers volume, into which it writes route files that Goma hot-reloads.
func controlPlaneSpec(m *Manifest, name, image string) docker.RunSpec {
	spec := docker.RunSpec{
		Name:  name,
		Image: image,
		Env: []string{
			"MIABI_ENV=production",
			"MIABI_PORT=9000",
			"MIABI_DB_HOST=" + ContainerPostgres,
			"MIABI_DB_USER=miabi",
			"MIABI_DB_PASSWORD=" + m.Secrets.DBPassword,
			"MIABI_DB_NAME=miabi",
			"MIABI_REDIS_ADDR=" + ContainerRedis + ":6379",
			"MIABI_REDIS_PASSWORD=" + m.Secrets.RedisPassword,
			"MIABI_JWT_SECRET=" + m.Secrets.JWTSecret,
			"MIABI_ENCRYPTION_KEY=" + m.Secrets.EncryptionKey,
			"MIABI_ADMIN_EMAIL=" + m.Secrets.AdminEmail,
			"MIABI_ADMIN_PASSWORD=" + m.Secrets.AdminPassword,
			"MIABI_WEB_URL=" + m.WebURL,
			"MIABI_CORS_ORIGINS=" + m.WebURL,
			// Where remote nodes and agents dial back: the node gateways' route provider
			// fetches from it, the agent connects to it, the registry points at it for auth.
			// Defaults to WebURL, but need not equal it — a node on a private network may
			// reach the control plane at an address the public panel URL never resolves to.
			"MIABI_CONTROL_URL=" + m.ControlURL,
			"MIABI_PROXY_NETWORK=" + m.Network.Name,
			"MIABI_ACME_EMAIL=" + m.ACMEEmail,
			"MIABI_GOMA_PROVIDER_DIR=" + gomaProviders,
			// Gateways Miabi provisions on remote nodes run the same Goma image as the
			// central one, so a node can never drift onto a different version.
			"MIABI_NODE_GATEWAY_IMAGE=" + m.Images.Gateway,
			"MIABI_RUNNER_IMAGE=" + m.Images.Runner,
		},
		Binds: []docker.BindMount{
			{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		},
		Mounts: map[string]string{
			VolumeLogs:             "/var/lib/miabi/logs",
			VolumeGatewayProviders: gomaProviders,
		},
		Networks:      []string{m.Network.Name},
		RestartPolicy: "unless-stopped",
		// /healthz is the liveness probe: it answers 200 as soon as the server is
		// serving, and asserts nothing about dependencies. That is exactly right for a
		// Docker healthcheck, which drives restarts — /readyz also pings Postgres,
		// Redis and Docker, so a database blip would mark the panel unhealthy and
		// restart it, which cannot fix a database and only adds an outage to an outage.
		//
		// The crash-loop case this gate exists for is still caught: a control plane that
		// cannot reach its database EXITS, so it never serves /healthz either.
		//
		// (Probing "/" worked too, but served the whole SPA to answer a health check.)
		Healthcheck: &docker.HealthcheckSpec{
			Test:        []string{"CMD-SHELL", "wget -qO- http://127.0.0.1:9000/healthz >/dev/null 2>&1 || exit 1"},
			Interval:    10 * time.Second,
			Timeout:     5 * time.Second,
			Retries:     6,
			StartPeriod: 30 * time.Second, // first boot runs migrations
		},
		Labels: docker.PlatformLabels(docker.RoleControlPlane, docker.ManagedByMiabi, nil),
	}
	// Read-only host procfs, so the admin Nodes page reports real host CPU/memory.
	// Optional: some hosts refuse the bind outright (a rootless daemon, a hardened
	// host, a socket proxy that forbids host binds). Without it Miabi reads its own
	// /proc, which inside a container already reflects host CPU/memory — the page keeps
	// working, so this is a graceful degradation and not a feature switch.
	if m.HostProc == nil || *m.HostProc {
		spec.Binds = append(spec.Binds, docker.BindMount{
			Source: "/proc", Target: "/host/proc", ReadOnly: true,
		})
	}
	if m.DockerGID != "" {
		spec.GroupAdd = []string{m.DockerGID}
	}
	// The built-in OCI registry. Written ONLY when enabled: any non-empty
	// MIABI_REGISTRY_* value is a one-way override that pins the setting out of the
	// admin UI's reach. Emitting `MIABI_REGISTRY_ENABLED=false` would therefore not
	// mean "off, change it in the UI" — it would mean "off, and you may never turn it
	// on from the UI". Absent is the only way to say "the UI decides".
	//
	// MIABI_REGISTRY_AUTH_URL needs no value here: its default is http://miabi:9000,
	// which is exactly the control plane's container name on the shared network.
	if m.Registry.Enabled {
		spec.Env = append(spec.Env,
			"MIABI_REGISTRY_ENABLED=true",
			"MIABI_REGISTRY_HOST="+m.Registry.Host,
		)
	}

	// Forwarded from gateway.env: Miabi encrypts the gateway config that Goma decrypts,
	// so the key must be identical on both sides.
	if v := m.Gateway.Env[gomaConfigEncryptionKey]; v != "" {
		spec.Env = append(spec.Env, gomaConfigEncryptionKey+"="+v)
	}

	// The operator's own variables, last. Normalize has already refused any key Miabi
	// sets above, so this can never shadow one — there are no duplicate keys in the
	// container's environment, and therefore no ordering rule to reason about.
	//
	// Sorted, because Go map iteration is random and specHash would otherwise differ
	// on every run, recreating the whole stack on every converge.
	for _, k := range sortedKeys(m.Env) {
		spec.Env = append(spec.Env, k+"="+m.Env[k])
	}
	return spec
}

// gomaConfigEncryptionKey must hold the SAME value in two containers: Miabi encrypts
// the gateway config it writes, and Goma decrypts it. Set on only one side, routing
// breaks silently — Goma reads a config it cannot decrypt.
//
// Its home is gateway.env (it configures Goma), and it is forwarded from there to the
// control plane. The top-level env: refuses it and points here, so there is exactly
// one place to set it and no way for the two halves to disagree.
const gomaConfigEncryptionKey = "GOMA_CONFIG_ENCRYPTION_KEY"

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// gatewaySpec — Goma: TLS termination, ACME, and routing to every app.
//
// Ports are published ONLY for the live container. A rollout starts the new image
// under <name>-test first, and two containers cannot both bind :443 — so the test
// container runs portless, proving the image boots before it is trusted with the
// ports. That is the whole reason Build takes the name.
func gatewaySpec(m *Manifest, name, image string) docker.RunSpec {
	spec := docker.RunSpec{
		Name:  name,
		Image: image,
		Env:   gatewayConfigEnv(m),
		Mounts: map[string]string{
			VolumeGatewayCerts:     "/etc/letsencrypt",
			VolumeGatewayProviders: gomaProviders,
		},
		Networks:      []string{m.Network.Name},
		RestartPolicy: "unless-stopped",
		Healthcheck: &docker.HealthcheckSpec{
			Test:        []string{"CMD-SHELL", "wget -qO- http://127.0.0.1/healthz >/dev/null 2>&1 || exit 1"},
			Interval:    10 * time.Second,
			Timeout:     5 * time.Second,
			Retries:     5,
			StartPeriod: 15 * time.Second,
		},
		Labels: docker.PlatformLabels(docker.RoleGateway, docker.ManagedByMiabi, nil),
	}
	// The config file itself, read-only, from the host.
	if m.gatewayHostConfig != "" {
		spec.Binds = append(spec.Binds, docker.BindMount{
			Source: m.gatewayHostConfig, Target: gomaConfigFile, ReadOnly: true,
		})
	}
	// The operator's own variables — anything their config interpolates, plus
	// GOMA_CONFIG_ENCRYPTION_KEY. Normalize has already refused the keys Miabi sets
	// above, so these can never shadow one.
	for _, k := range sortedKeys(m.Gateway.Env) {
		spec.Env = append(spec.Env, k+"="+m.Gateway.Env[k])
	}
	applyTimezone(m, &spec)
	if name == ContainerGateway {
		spec.Ports = map[string]string{"80/tcp": "80", "443/tcp": "443"}
	}
	return spec
}

// applyTimezone copies TZ from env: onto a component that is not the control plane.
//
// TZ is the one env: entry that belongs to the WHOLE stack. Postgres, Redis and Goma
// all read it (they ship tzdata), and applying it to the control plane alone would
// timestamp Miabi's logs in one zone and the logs of every container it manages in
// another — which is precisely the situation you least want when reading a timeline
// across them.
func applyTimezone(m *Manifest, spec *docker.RunSpec) {
	if tz := m.Env[envTimezone]; tz != "" {
		spec.Env = append(spec.Env, envTimezone+"="+tz)
	}
}

// specHash fingerprints what we ASKED Docker for, so converge can tell "already what
// the manifest says" from "changed".
//
// It hashes the request, never the running container. Docker normalizes a spec on
// the way in — reordering env, rewriting port and mount syntax, filling defaults — so
// comparing a fresh spec against an inspected container means re-deriving all of
// that, and every mismatch would surface as a spurious recreate. For Postgres a
// spurious recreate is a database restart. Hashing the request makes the comparison
// exact by construction: same manifest, same spec, same hash, no action.
//
// The hash deliberately covers the secrets (they are in Env), so rotating one
// recreates the containers that carry it.
func specHash(spec docker.RunSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "image=%s\n", spec.Image)
	fmt.Fprintf(&b, "cmd=%q\nentrypoint=%q\n", spec.Cmd, spec.Entrypoint)
	fmt.Fprintf(&b, "restart=%s\nuser=%s\ngroups=%q\n", spec.RestartPolicy, spec.User, spec.GroupAdd)

	// Sorted: Go map iteration is random, and an unsorted hash would differ on every
	// run — turning every converge into a full recreate of the stack.
	writeSorted(&b, "env", spec.Env)
	writeSorted(&b, "networks", spec.Networks)
	writeSortedMap(&b, "mounts", spec.Mounts)
	writeSortedMap(&b, "ports", spec.Ports)
	for _, bm := range spec.Binds {
		fmt.Fprintf(&b, "bind=%s:%s:%v\n", bm.Source, bm.Target, bm.ReadOnly)
	}
	if spec.Healthcheck != nil {
		fmt.Fprintf(&b, "health=%q\n", spec.Healthcheck.Test)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:12]) // 24 hex chars: plenty to detect change, short in `docker inspect`
}

func writeSorted(b *strings.Builder, key string, vals []string) {
	cp := append([]string(nil), vals...)
	sort.Strings(cp)
	for _, v := range cp {
		fmt.Fprintf(b, "%s=%s\n", key, v)
	}
}

func writeSortedMap(b *strings.Builder, key string, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s=%s:%s\n", key, k, m[k])
	}
}
