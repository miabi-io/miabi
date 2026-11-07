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
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/miabi-io/miabi/pkg/stack/docker"
)

// Goma expands ${...} at runtime so the file holds no secret; a test keeps it identical to the Compose goma.yml.
//
//go:embed assets/goma.yml
var gomaConfig []byte

// GomaConfig exposes the embedded config so the drift test can compare it.
func GomaConfig() []byte { return gomaConfig }

const (
	gomaConfigDir  = "/etc/goma"
	gomaConfigFile = gomaConfigDir + "/goma.yml"
	gomaProviders  = gomaConfigDir + "/providers"
	// The first directory Goma searches for country.mmdb (or GeoLite2-Country.mmdb).
	gomaGeoIPDir = gomaConfigDir + "/geoip"
)

// Not on the proxy network: a tenant able to dial it would own every workspace via the superuser password.
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
		Networks:      []string{m.InternalNetwork.Name},
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

// Private network only: write access to the asynq queue is code execution in the worker.
func redisSpec(m *Manifest, name, image string) docker.RunSpec {
	spec := docker.RunSpec{
		Name:          name,
		Image:         image,
		Cmd:           []string{"redis-server", "--requirepass", m.Secrets.RedisPassword},
		Mounts:        map[string]string{VolumeRedisData: "/data"},
		Networks:      []string{m.InternalNetwork.Name},
		RestartPolicy: "unless-stopped",
		Healthcheck: &docker.HealthcheckSpec{
			// With requirepass set an unauthenticated PING answers NOAUTH and the container would stay unhealthy.
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

// Not on the proxy network: :9000 there would bypass route middlewares and be an SSRF target for any app.
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
			// Defaults to WebURL but a private-network node may reach the control plane at another address.
			"MIABI_CONTROL_URL=" + m.ControlURL,
			"MIABI_PROXY_NETWORK=" + m.Network.Name,
			// Lets out-of-process helpers reach Postgres by name; empty on Compose means the proxy network is all there is.
			"MIABI_INTERNAL_NETWORK=" + m.InternalNetwork.Name,
			"MIABI_ACME_EMAIL=" + m.ACMEEmail,
			"MIABI_GOMA_PROVIDER_DIR=" + gomaProviders,
			// Remote node gateways run the central gateway's image so a node can never drift onto another version.
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
		Networks:      []string{m.InternalNetwork.Name},
		RestartPolicy: "unless-stopped",
		// /healthz asserts no dependencies; /readyz pings Postgres, so a blip would restart the panel.
		Healthcheck: &docker.HealthcheckSpec{
			Test:        []string{"CMD-SHELL", "wget -qO- http://127.0.0.1:9000/healthz >/dev/null 2>&1 || exit 1"},
			Interval:    10 * time.Second,
			Timeout:     5 * time.Second,
			Retries:     6,
			StartPeriod: 30 * time.Second, // first boot runs migrations
		},
		Labels: docker.PlatformLabels(docker.RoleControlPlane, docker.ManagedByMiabi, nil),
	}
	// Optional: without the bind Miabi reads its own /proc, which already reflects the host.
	if m.HostProc == nil || *m.HostProc {
		spec.Binds = append(spec.Binds, docker.BindMount{
			Source: "/proc", Target: "/host/proc", ReadOnly: true,
		})
	}
	if m.DockerGID != "" {
		spec.GroupAdd = []string{m.DockerGID}
	}
	// Written only when enabled: absent already reads as false, and setting a key pins it against the UI.
	if m.Registry.Enabled {
		spec.Env = append(spec.Env,
			"MIABI_REGISTRY_ENABLED=true",
			"MIABI_REGISTRY_HOST="+m.Registry.Host,
		)
	}

	// Miabi encrypts the gateway config that Goma decrypts, so the key must match on both sides.
	if v := m.Gateway.Env[gomaConfigEncryptionKey]; v != "" {
		spec.Env = append(spec.Env, gomaConfigEncryptionKey+"="+v)
	}

	// Compiled to variables the control plane already reads, so no separate reconciler is needed.
	spec.Env = append(spec.Env, installEnv(m)...)
	if proxies := m.Install.ServerTrustedProxies; len(proxies) > 0 {
		spec.Env = append(spec.Env, envTrustedProxies+"="+strings.Join(proxies, ","))
	}

	// Normalize refused Miabi's own keys so these can't shadow them; sorted to keep specHash stable.
	for _, k := range sortedKeys(m.Env) {
		spec.Env = append(spec.Env, k+"="+m.Env[k])
	}
	return spec
}

// Must match in both containers; its home is gateway.env and the top-level env refuses it.
const gomaConfigEncryptionKey = "GOMA_CONFIG_ENCRYPTION_KEY"

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Only the live container publishes ports, since a rollout's <name>-test can't also bind :443.
func gatewaySpec(m *Manifest, name, image string) docker.RunSpec {
	spec := docker.RunSpec{
		Name:  name,
		Image: image,
		Env:   gatewayConfigEnv(m),
		Mounts: map[string]string{
			VolumeGatewayCerts:     "/etc/letsencrypt",
			VolumeGatewayProviders: gomaProviders,
		},
		Networks:      []string{m.InternalNetwork.Name, m.Network.Name},
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
	// Bound only once created, so Docker never creates it as root.
	if m.gatewayHostGeoIP != "" {
		spec.Binds = append(spec.Binds, docker.BindMount{
			Source: m.gatewayHostGeoIP, Target: gomaGeoIPDir, ReadOnly: true,
		})
	}
	// Normalize already refused Miabi's own keys, so these can never shadow one.
	for _, k := range sortedKeys(m.Gateway.Env) {
		spec.Env = append(spec.Env, k+"="+m.Gateway.Env[k])
	}
	applyTimezone(m, &spec)
	if name == ContainerGateway {
		spec.Ports = map[string]string{"80/tcp": "80", "443/tcp": "443"}
	}
	return spec
}

// TZ is stack-wide; applying it only to the control plane would split log timezones.
func applyTimezone(m *Manifest, spec *docker.RunSpec) {
	if tz := m.Env[envTimezone]; tz != "" {
		spec.Env = append(spec.Env, envTimezone+"="+tz)
	}
}

// Hashes the requested spec, since Docker normalizes it and inspected comparisons cause spurious recreates.
func specHash(spec docker.RunSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "image=%s\n", spec.Image)
	fmt.Fprintf(&b, "cmd=%q\nentrypoint=%q\n", spec.Cmd, spec.Entrypoint)
	fmt.Fprintf(&b, "restart=%s\nuser=%s\ngroups=%q\n", spec.RestartPolicy, spec.User, spec.GroupAdd)

	// Unsorted, Go map order would change the hash and recreate the stack on every converge.
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
