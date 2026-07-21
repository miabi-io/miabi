// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformstack

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultManifestPath is where the installed stack's desired state lives.
//
// It has to be a file on the host, and it cannot be the database: Postgres is
// itself a component of the stack, so the CLI cannot read the database to learn how
// to start the database. Nor can it be a table Miabi owns — `miabi install` runs
// before Miabi exists.
//
// Be honest about what this is: it is compose.yaml + .env under another name. The
// win is not that the file is gone. The win is that Miabi *writes* it, so it can
// never disagree with what Miabi actually did — which is exactly the failure mode of
// having Miabi patch a compose file that Compose owns.
const DefaultManifestPath = "/etc/miabi/stack.yaml"

// ManifestPathEnv overrides DefaultManifestPath (tests, rootless installs, a second
// stack on one host).
const ManifestPathEnv = "MIABI_STACK_FILE"

// manifestMode is 0600: the file holds the database password, the JWT secret and the
// encryption key in plain text, exactly as .env does today. Anyone who can read it
// owns the install.
//
// This is not a weaker position than Compose's. Both are equally protected by file
// permissions, and both are moot against an attacker who already has the Docker
// socket — the socket is root. Docker secrets would move the secret, not remove the
// exposure, so long as the same process holds the socket.
const manifestMode = 0o600

// Manifest is the installed stack's desired state.
type Manifest struct {
	// Version is the manifest schema, not the Miabi version.
	Version int `yaml:"version"`

	Domain    string `yaml:"domain"`
	WebURL    string `yaml:"web_url"`
	ACMEEmail string `yaml:"acme_email"`

	// ControlURL is the URL remote nodes and agents reach this control plane at: the
	// node gateways' route provider fetches from it, the agent dials it, and the
	// registry points at it for auth.
	//
	// Defaults to WebURL, which is right for a single public hostname. It is separate
	// because it does not have to be: a node on a private network may reach the control
	// plane at an internal address the public panel URL never resolves to, and pinning
	// the two together would force that traffic out over the internet and back.
	ControlURL string `yaml:"control_url"`

	Network  NetworkConfig `yaml:"network"`
	Images   Images        `yaml:"images"`
	Secrets  Secrets       `yaml:"secrets"`
	Registry Registry      `yaml:"registry"`
	Gateway  Gateway       `yaml:"gateway"`

	// Env are extra environment variables for the control plane — anything Miabi reads
	// that this manifest does not already model: MIABI_SMTP_*, MIABI_LOG_LEVEL,
	// MIABI_OAUTH_*, TZ, HTTP_PROXY, and so on.
	//
	// It may not contain any variable Miabi sets ITSELF (the database password, the
	// domain, the encryption key…). Those are refused rather than merged: a manifest
	// where `secrets.db_password` says one thing and `env.MIABI_DB_PASSWORD` says
	// another has two sources of truth, and whichever loses is a silent, invisible
	// misconfiguration. Normalize enforces this — see normalizeEnv.
	Env map[string]string `yaml:"env,omitempty"`

	// DockerGID is the host's docker group, added to the control plane so it can read
	// /var/run/docker.sock without running as root.
	DockerGID string `yaml:"docker_gid,omitempty"`

	// HostProc binds the host's /proc read-only into the control plane, so the admin
	// Nodes page can report real host CPU and memory.
	//
	// A POINTER, not a bool: absent must mean "on", and a plain bool's zero value is
	// false — every manifest written before this field existed would silently turn the
	// bind off on the next converge. nil is resolved to true in Normalize and written
	// back explicitly, so the file always says which it is.
	//
	// Turning it off is safe. Miabi falls back to its own /proc, which inside a
	// container already reflects host CPU/memory (procfs is not namespaced for those),
	// so the Nodes page keeps working. Set it false where the bind is refused outright:
	// a rootless daemon, a hardened host, or a socket proxy that forbids host binds.
	HostProc *bool `yaml:"host_proc"`

	// gatewayHostConfig is the gateway config's path AS THE DOCKER DAEMON SEES IT,
	// resolved by EnsureGatewayConfig. Not serialized: it describes this run's
	// environment (are we in a container? which host dir is bound?), not the desired
	// state, and writing it into stack.yaml would make the manifest wrong the moment it
	// were used from somewhere else.
	gatewayHostConfig string `yaml:"-"`
	// gatewayHostGeoIP is the GeoIP database's host path (as the daemon sees it),
	// resolved by EnsureGatewayConfig when a database is present/provisioned. Empty
	// when GeoIP is off or unavailable — the gateway simply mounts no database and
	// analytics runs without country. Not serialized, for the same reason as above.
	gatewayHostGeoIP string `yaml:"-"`
}

type NetworkConfig struct {
	Name   string `yaml:"name"`
	Subnet string `yaml:"subnet"`
}

// Gateway configures Goma: its config file and its environment.
//
// The config is a FILE ON THE HOST, bind-mounted read-only — exactly as
// examples/compose/compose.yaml does it (`./goma/goma.yml:/etc/goma/goma.yml:ro`). It is not
// copied into a volume: copying makes the volume the source of truth and the host
// file a stale duplicate that every converge silently overwrites, so an operator
// following goma.yml's own instructions ("uncomment this to restrict the panel to
// trusted IPs") would watch their edit disappear on the next `miabi install`.
type Gateway struct {
	// Config is the gateway config file, relative to the manifest's own directory
	// (so it sits next to stack.yaml and is backed up with it). Absolute paths are
	// taken as-is. Empty means goma.yml.
	Config string `yaml:"config,omitempty"`

	// ConfigSHA is the digest of the DEFAULT config Miabi last wrote to that file.
	//
	// It is what lets an unmodified config keep receiving upstream improvements while
	// a customized one is never clobbered: if the file still hashes to this, nobody
	// touched it and a newer release's default may replace it; if it does not, the
	// operator edited it and Miabi leaves it alone. Without this, either customization
	// is impossible (we always overwrite) or every install is frozen on the base
	// config it shipped with (we never overwrite) — and nobody would be told which.
	ConfigSHA string `yaml:"config_sha,omitempty"`

	// Env is the gateway container's environment: anything the config interpolates
	// (`${MY_UPSTREAM}`), plus GOMA_CONFIG_ENCRYPTION_KEY.
	//
	// Variables Miabi sets itself (the domain, the ACME email, the Redis password) are
	// refused here, exactly as in the top-level env: — a manifest where `domain` says
	// one thing and `gateway.env.MIABI_DOMAIN` says another has two sources of truth.
	Env map[string]string `yaml:"env,omitempty"`
}

// Registry configures the built-in OCI registry.
//
// Only written into the control plane's environment when Enabled. That is not a
// stylistic choice: a non-empty MIABI_REGISTRY_* value is a ONE-WAY OVERRIDE — it
// pins the setting and the admin UI can no longer change it. Leaving the keys absent
// is what keeps the registry a UI-managed setting, which is the right default for an
// operator who has not asked for it. examples/compose/compose.yaml and install.sh take exactly
// the same position.
type Registry struct {
	Enabled bool `yaml:"enabled"`
	// Host is the registry's own public hostname (registry.example.com). It gets a
	// DNS record and its own TLS certificate, separate from the panel's.
	Host string `yaml:"host,omitempty"`
}

// Images pins every image the stack runs. Each is a full ref (repo:tag), not a bare
// tag: the point of pinning is that `miabi install` on two hosts a month apart
// produces the same stack.
type Images struct {
	Miabi    string `yaml:"miabi"`
	Postgres string `yaml:"postgres"`
	Redis    string `yaml:"redis"`
	Gateway  string `yaml:"gateway"`
	// Runner is not run by the stack; it is the image shown in the runner enrollment
	// command, so it belongs to the install's identity even though nothing here starts
	// it. It is therefore the one image whose default floats on :latest — see
	// DefaultRunnerImage. install.sh still pins it to the tested version.
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

// ManifestPath resolves the manifest location.
func ManifestPath() string {
	if p := strings.TrimSpace(os.Getenv(ManifestPathEnv)); p != "" {
		return p
	}
	return DefaultManifestPath
}

// Load reads the manifest. A missing file returns ErrNotInstalled, which callers
// distinguish from a corrupt one — "you have not installed yet" and "your install
// is unreadable" need very different messages.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
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

// Save writes the manifest atomically at 0600, creating its directory.
//
// Atomic because this file is the only record of the install's secrets: a partial
// write from a killed process would lose the database password, and with it the
// database.
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
# WRITTEN BY MIABI. Hand-edits are respected — re-run 'miabi install' and the stack
# converges to whatever this says — but comments you add here are NOT preserved: the
# file is rewritten from scratch on the next install/update.
#
# This file holds the database password, JWT secret and encryption key in plain
# text, at mode 0600. It is the only copy. Back it up somewhere safe: without it you
# cannot decrypt the secrets Miabi has stored, and the database is unrecoverable.
#
#   miabi status          show the running stack against this file
#   miabi install         converge the stack to this file (safe to re-run)
#   miabi update          roll the stack forward to a newer image
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
		// crypto derives the AES key with sha256 over whatever string it is given, so
		// the length is not load-bearing — but 32 bytes matches the `openssl rand -hex
		// 32` the docs and .env.example tell people to use, and an install should not
		// look weaker than the instructions it replaces.
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
