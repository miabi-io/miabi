// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package stack

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultConfigPath is where the installed stack's desired state lives. It has to be a file on the
// host, not the database: Postgres is itself a component, so the CLI cannot read the database to learn
// how to start the database. Miabi *writes* it, so it can never disagree with what Miabi actually did.
const DefaultConfigPath = "/etc/miabi/miabi.yaml"

// LegacyConfigPath is the pre-rename location. It is no longer read implicitly — but it is still
// *detected*, so an install that predates the rename gets told to run `miabi stack migrate-config`
// instead of "not installed". It is never deleted on the operator's behalf: it holds the database
// password, and backup scripts may point at it.
const LegacyConfigPath = "/etc/miabi/stack.yaml"

// ConfigPathEnv overrides the resolved path (tests, rootless installs, a second stack on one host).
// ManifestPathEnv is the older spelling and is still honoured.
const (
	ConfigPathEnv   = "MIABI_CONFIG_FILE"
	ManifestPathEnv = "MIABI_STACK_FILE"
)

// manifestMode is 0600: the file holds the database password, JWT secret and encryption key in plain text,
// exactly as .env does today, so anyone who can read it owns the install. This is not weaker than Compose's —
// both are moot against an attacker who already has the Docker socket, which is root.
const manifestMode = 0o600

// Manifest is the installed stack's desired state.
type Manifest struct {
	// Version is the manifest schema, not the Miabi version.
	Version int `yaml:"version"`

	Domain    string `yaml:"domain"`
	WebURL    string `yaml:"web_url"`
	ACMEEmail string `yaml:"acme_email"`

	// ControlURL is the URL remote nodes and agents reach this control plane at. It defaults to WebURL,
	// which is right for a single public hostname, but is separate because a node on a private network may
	// reach the control plane at an address the public panel URL never resolves to.
	ControlURL string `yaml:"control_url"`

	Network NetworkConfig `yaml:"network"`
	// InternalNetwork is the PRIVATE network the platform's own components talk over — the control
	// plane to Postgres and Redis, the gateway to both of those. Network above stays what it always
	// was: the shared proxy fabric routed apps join, which is why nothing platform-internal may sit
	// on it. Added after the fact, so it is a sibling key rather than a nested one: every manifest
	// written before this release keeps parsing and gains the network on its next converge.
	InternalNetwork NetworkConfig `yaml:"internal_network"`

	Images   Images   `yaml:"images"`
	Secrets  Secrets  `yaml:"secrets"`
	Registry Registry `yaml:"registry"`
	Gateway  Gateway  `yaml:"gateway"`

	// Env are extra environment variables for the control plane — anything Miabi reads that this manifest
	// does not already model. It may not contain a variable Miabi sets itself: two sources of truth for the
	// database password means whichever loses is a silent misconfiguration. Normalize enforces this.
	Env map[string]string `yaml:"env,omitempty"`

	// DockerGID is the host's docker group, added to the control plane so it can read
	// /var/run/docker.sock without running as root.
	DockerGID string `yaml:"docker_gid,omitempty"`

	// HostProc binds the host's /proc read-only into the control plane, so the Nodes page can report real
	// host CPU and memory. A POINTER, because absent must mean "on" and a bool's zero value is false.
	// Turning it off is safe — Miabi falls back to its own /proc, which already reflects host CPU/memory.
	HostProc *bool `yaml:"host_proc"`

	// gatewayHostConfig is the gateway config's path AS THE DOCKER DAEMON SEES IT, resolved by
	// EnsureGatewayConfig. Not serialized: it describes this run's environment — are we in a container, which
	// host dir is bound — not the desired state, so writing it into miabi.yaml would make the manifest wrong.
	gatewayHostConfig string `yaml:"-"`
	// gatewayHostGeoIP is the GeoIP database's host path as the daemon sees it, resolved by EnsureGatewayConfig
	// when a database is present. Empty when GeoIP is off or unavailable — the gateway mounts no database and
	// analytics runs without country. Not serialized, for the same reason as gatewayHostConfig.
	gatewayHostGeoIP string `yaml:"-"`
}

type NetworkConfig struct {
	Name   string `yaml:"name"`
	Subnet string `yaml:"subnet"`
}

// Gateway configures Goma: its config file and its environment. The config is a FILE ON THE HOST,
// bind-mounted read-only, not copied into a volume — copying makes the volume the source of truth and
// the host file a stale duplicate every converge silently overwrites.
type Gateway struct {
	// Config is the gateway config file, relative to the manifest's own directory
	// (so it sits next to miabi.yaml and is backed up with it). Absolute paths are
	// taken as-is. Empty means goma.yml.
	Config string `yaml:"config,omitempty"`

	// ConfigSHA is the digest of the DEFAULT config Miabi last wrote to that file. It is what lets an
	// unmodified config keep receiving upstream improvements while a customized one is never clobbered.
	// Without it, either customization is impossible or every install is frozen on its shipped config.
	ConfigSHA string `yaml:"config_sha,omitempty"`

	// Env is the gateway container's environment: anything the config interpolates, plus
	// GOMA_CONFIG_ENCRYPTION_KEY. Variables Miabi sets itself are refused here, exactly as in the top-level
	// env — a manifest where `domain` and `gateway.env.MIABI_DOMAIN` disagree has two sources of truth.
	Env map[string]string `yaml:"env,omitempty"`
}

// Registry configures the built-in OCI registry. This manifest is the ONLY place enablement and the
// hostname live: the host anchors every stored image reference and decides which workspace owns an
// image, so the admin UI shows them read-only. The keys are written only when Enabled.
type Registry struct {
	Enabled bool `yaml:"enabled"`
	// Host is the registry's own public hostname (registry.example.com). It gets a
	// DNS record and its own TLS certificate, separate from the panel's.
	Host string `yaml:"host,omitempty"`
}

// Images pins every image the stack runs. Each is a full ref (repo:tag), not a bare
// tag: the point of pinning is that `miabi setup` on two hosts a month apart
// produces the same stack.
type Images struct {
	Miabi    string `yaml:"miabi"`
	Postgres string `yaml:"postgres"`
	Redis    string `yaml:"redis"`
	Gateway  string `yaml:"gateway"`
	// Runner is not run by the stack; it is the image shown in the runner enrollment command, so it belongs
	// to the install's identity even though nothing here starts it. It is therefore the one image whose
	// default floats on :latest — see DefaultRunnerImage. install.sh still pins it to the tested version.
	Runner string `yaml:"runner"`
}

type Secrets struct {
	DBPassword    string `yaml:"db_password"`
	RedisPassword string `yaml:"redis_password"`
	JWTSecret     string `yaml:"jwt_secret"`
	EncryptionKey string `yaml:"encryption_key"`
	AdminEmail    string `yaml:"admin_email"`
	AdminPassword string `yaml:"admin_password"`
}

// ManifestPath resolves the manifest location: MIABI_CONFIG_FILE, else the older MIABI_STACK_FILE,
// else /etc/miabi/miabi.yaml. The legacy /etc/miabi/stack.yaml is no longer read implicitly — Load
// detects it and says how to migrate, which beats silently operating on a path the operator was
// told is gone.
func ManifestPath() string {
	for _, env := range []string{ConfigPathEnv, ManifestPathEnv} {
		if p := strings.TrimSpace(os.Getenv(env)); p != "" {
			return p
		}
	}
	return DefaultConfigPath
}

// Load reads the manifest. A missing file returns ErrNotInstalled, which callers
// distinguish from a corrupt one — "you have not installed yet" and "your install
// is unreadable" need very different messages.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// A pre-rename install is not "not installed" — it is one command away from working, and
		// saying so is the difference between a 10-second fix and a support thread.
		if path == DefaultConfigPath {
			if _, lerr := os.Stat(LegacyConfigPath); lerr == nil {
				return nil, fmt.Errorf("%w: found it at %s, but Miabi now reads %s\n\n"+
					"  Rename it:  sudo miabi stack migrate-config\n"+
					"  Or point at it directly:  --file %s",
					ErrLegacyConfig, LegacyConfigPath, DefaultConfigPath, LegacyConfigPath)
			}
		}
		return nil, fmt.Errorf("%w (looked in %s)", ErrNotInstalled, path)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.Version == 0 {
		return nil, fmt.Errorf("%s has no version — it is not a Miabi stack manifest", path)
	}
	if m.Version > CurrentVersion {
		return nil, fmt.Errorf("%s is version %d but this miabi understands up to %d — upgrade the CLI",
			path, m.Version, CurrentVersion)
	}
	return &m, nil
}

// Save writes the manifest atomically at 0600, creating its directory. Atomic because this file is the
// only record of the install's secrets: a partial write from a killed process would lose the database
// password, and with it the database.
func Save(path string, m *Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	body := append([]byte(manifestHeader), b...)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, manifestMode); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	// Rename preserves the temp file's mode, but an existing target may predate this
	// code (or a careless chmod); re-assert it rather than assume.
	if err := os.Chmod(path, manifestMode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

const manifestHeader = `# Miabi — installed stack manifest.
#
# WRITTEN BY MIABI. Hand-edits are respected — re-run 'miabi setup' and the stack
# converges to whatever this says — but comments you add here are NOT preserved: the
# file is rewritten from scratch on the next setup/upgrade.
#
# This file holds the database password, JWT secret and encryption key in plain
# text, at mode 0600. It is the only copy. Back it up somewhere safe: without it you
# cannot decrypt the secrets Miabi has stored, and the database is unrecoverable.
#
#   miabi stack status    show the running stack against this file
#   miabi setup           converge the stack to this file (safe to re-run)
#   miabi upgrade         roll the stack forward to a newer image
#
# Extra settings go under 'env:' — anything Miabi reads that this file does not
# already model (SMTP, OAuth, HTTP_PROXY, …). Two are seeded for you:
#
#   TZ                UTC by default. Applies to the WHOLE stack (Miabi, Postgres,
#                     Redis and the gateway), so their log timestamps agree. Any
#                     zoneinfo name: Europe/Paris, America/New_York, …
#   MIABI_LOG_LEVEL   how chatty Miabi's own logs are: debug | info | warn | error.
#
# host_proc: false stops Miabi binding the host's /proc read-only. Set it where the
# bind is refused (a rootless daemon, a hardened host, a socket proxy that forbids
# host binds). The Nodes page keeps working: Miabi falls back to its own /proc, which
# inside a container already reflects host CPU and memory.
#
#   env:
#     TZ: Europe/Paris
#     MIABI_LOG_LEVEL: debug
#     MIABI_SMTP_HOST: smtp.example.com
#     MIABI_SMTP_PORT: "587"
#
# Variables Miabi sets itself (the database password, the domain, the encryption key,
# the registry) are REFUSED there rather than merged — having them in two places is
# how a stack ends up with a password that does not open its own database.
#
`

// ImagePin returns a pointer to the manifest field pinning the named component's
// image, so an update can write the new value back to the right place. ok is false
// for a name that is not a stack component.
func (m *Manifest) ImagePin(container string) (*string, bool) {
	switch container {
	case ContainerControlPlane:
		return &m.Images.Miabi, true
	case ContainerPostgres:
		return &m.Images.Postgres, true
	case ContainerRedis:
		return &m.Images.Redis, true
	case ContainerGateway:
		return &m.Images.Gateway, true
	}
	return nil, false
}

// ImageFor is the read-only form of ImagePin, for drift reporting.
func (m *Manifest) ImageFor(container string) (string, bool) {
	p, ok := m.ImagePin(container)
	if !ok {
		return "", false
	}
	return *p, true
}

// GenerateSecrets fills any secret that is still empty, leaving existing values
// alone so a re-install never rotates a live install's credentials out from under
// it (which would lock Miabi out of its own database).
func (m *Manifest) GenerateSecrets() error {
	for _, f := range []struct {
		dst   *string
		bytes int
	}{
		{&m.Secrets.DBPassword, 24},
		{&m.Secrets.RedisPassword, 24},
		{&m.Secrets.JWTSecret, 32},
		// crypto derives the AES key with sha256 over whatever string it is given, so the length is not
		// load-bearing — but 32 bytes matches the `openssl rand -hex 32` the docs and .env.example tell people to
		// use, and an install should not look weaker than the instructions it replaces.
		{&m.Secrets.EncryptionKey, 32},
	} {
		if *f.dst != "" {
			continue
		}
		v, err := randomHex(f.bytes)
		if err != nil {
			return err
		}
		*f.dst = v
	}
	if m.Secrets.AdminPassword == "" {
		v, err := randomHex(12)
		if err != nil {
			return err
		}
		// Mixed case + a symbol so it satisfies a password policy that a bare hex
		// string would fail.
		m.Secrets.AdminPassword = "Mb!" + v
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}
