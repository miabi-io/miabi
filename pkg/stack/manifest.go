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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Must be a host file, not the DB: the CLI cannot read Postgres to learn how to start Postgres.
const DefaultConfigPath = "/etc/miabi/miabi.yaml"

// Still detected so old installs are told to migrate; never deleted since it holds the DB password.
const LegacyConfigPath = "/etc/miabi/stack.yaml"

// ManifestPathEnv is the older spelling and is still honoured.
const (
	ConfigPathEnv   = "MIABI_CONFIG_FILE"
	ManifestPathEnv = "MIABI_STACK_FILE"
)

// Same layout as /etc/miabi because everything else resolves relative to the manifest.
const UserConfigDir = ".miabi"

// Empty when there is no home directory, which the caller must handle.
func UserConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, UserConfigDir, "miabi.yaml")
}

// 0600 because it holds plaintext secrets; anyone with the Docker socket is root anyway.
const manifestMode = 0o600

// Manifest is the installed stack's desired state.
type Manifest struct {
	// Version is the manifest schema, not the Miabi version.
	Version int `yaml:"version"`

	Domain    string `yaml:"domain"`
	WebURL    string `yaml:"web_url"`
	ACMEEmail string `yaml:"acme_email"`

	// Separate from WebURL because a private-network node may reach the control plane at another address.
	ControlURL string `yaml:"control_url"`

	Network NetworkConfig `yaml:"network"`
	// A sibling key so pre-existing manifests keep parsing; nothing platform-internal may sit on the proxy network.
	InternalNetwork NetworkConfig `yaml:"internal_network"`

	Images   Images   `yaml:"images"`
	Secrets  Secrets  `yaml:"secrets"`
	Registry Registry `yaml:"registry"`
	Gateway  Gateway  `yaml:"gateway"`

	// May not contain a variable Miabi sets itself, or the loser of two sources of truth silently misconfigures.
	Env map[string]string `yaml:"env,omitempty"`

	DockerGID string `yaml:"docker_gid,omitempty"`

	// A pointer because absent must mean "on"; turning it off is safe since /proc already reflects the host.
	HostProc *bool `yaml:"host_proc"`

	// Not serialized: the flat schema never had these, and the document writer maps them field by field.
	Install InstallSettings `yaml:"-"`

	// Save writes back the shape Load read; converting is only ever explicit (ConvertFile).
	kinded bool `yaml:"-"`

	// Not serialized: it describes this run's environment, not desired state.
	gatewayHostConfig string `yaml:"-"`
	// Empty when GeoIP is off; not serialized for the same reason as gatewayHostConfig.
	gatewayHostGeoIP string `yaml:"-"`
}

type NetworkConfig struct {
	Name   string `yaml:"name"`
	Subnet string `yaml:"subnet"`
	IPv6   *bool  `yaml:"ipv6,omitempty"`
}

// The config is a host file bind-mounted read-only; copying it into a volume would make the host file stale.
type Gateway struct {
	// Relative to the manifest's directory so it is backed up with it; empty means goma.yml.
	Config string `yaml:"config,omitempty"`

	// Lets an unmodified config receive upstream updates while a customized one is never clobbered.
	ConfigSHA string `yaml:"config_sha,omitempty"`

	// Variables Miabi sets itself are refused here, so there is never a second source of truth.
	Env map[string]string `yaml:"env,omitempty"`
}

// The manifest is the only home of enablement and host, so the admin UI shows them read-only.
type Registry struct {
	Enabled bool `yaml:"enabled"`
	// It gets its own DNS record and TLS certificate, separate from the panel's.
	Host string `yaml:"host,omitempty"`
}

// Full refs, not bare tags, so `miabi setup` on two hosts a month apart produces the same stack.
type Images struct {
	Miabi    string `yaml:"miabi"`
	Postgres string `yaml:"postgres"`
	Redis    string `yaml:"redis"`
	Gateway  string `yaml:"gateway"`
	// Shown in the runner enrollment command, not run by the stack; the one image floating on :latest.
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

// Called only for a fresh install.
func (m *Manifest) GenerateGatewayConfigKey() error {
	if m.Gateway.Env[gomaConfigEncryptionKey] != "" {
		return nil
	}
	v, err := randomHex(32)
	if err != nil {
		return err
	}
	if m.Gateway.Env == nil {
		m.Gateway.Env = map[string]string{}
	}
	m.Gateway.Env[gomaConfigEncryptionKey] = v
	return nil
}

// An existing manifest is found before choosing by privilege, so a non-sudo read-only command still finds /etc.
func ManifestPath() string {
	for _, env := range []string{ConfigPathEnv, ManifestPathEnv} {
		if p := strings.TrimSpace(os.Getenv(env)); p != "" {
			return p
		}
	}
	return pickManifestPath(DefaultConfigPath, UserConfigPath(), fileExists, os.Geteuid() == 0)
}

func pickManifestPath(etc, user string, exists func(string) bool, root bool) string {
	for _, p := range []string{etc, user} {
		if p != "" && exists(p) {
			return p
		}
	}
	if root || user == "" {
		return etc
	}
	return user
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// A missing file returns ErrNotInstalled, distinct from a corrupt one.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// A pre-rename install is one command from working, so say so rather than "not installed".
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
	// The flat schema stays lenient: tightening files already on many hosts would turn an upgrade into an outage.
	if IsDocument(b) {
		d, derr := ParseDocument(b)
		if derr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, derr)
		}
		return d.Manifest(), nil
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

// Atomic because this is the only record of the install's secrets; a partial write would lose the database.
func Save(path string, m *Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	body, err := render(m)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, manifestMode); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	// An existing target may predate this code or a careless chmod, so re-assert the mode.
	if err := os.Chmod(path, manifestMode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// render writes the manifest in the shape it was read in.
func render(m *Manifest) ([]byte, error) {
	if m.kinded {
		b, err := MarshalDocument(NewDocument(m))
		if err != nil {
			return nil, err
		}
		return append([]byte(documentHeader), b...), nil
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return append([]byte(manifestHeader), b...), nil
}

// BackupSuffix names the copy ConvertFile leaves behind.
const BackupSuffix = ".bak"

// The copy is mandatory: it is the only record of the secrets and an older CLI can't read the new shape.
func ConvertFile(path string, m *Manifest) (bool, error) {
	if m.kinded {
		return false, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	bak := path + BackupSuffix
	if err := os.WriteFile(bak, b, manifestMode); err != nil {
		return false, fmt.Errorf("write %s: %w", bak, err)
	}
	m.kinded = true
	return true, nil
}

const documentHeader = `# Miabi — installed stack manifest (` + APIVersion + `).
#
# WRITTEN BY MIABI. Hand-edits are respected — re-run 'miabi setup' and the stack
# converges to whatever this says — but comments you add here are NOT preserved:
# the file is rewritten from scratch on the next setup/upgrade.
#
# This file holds the database password, JWT secret and encryption key in plain
# text, at mode 0600. It is the only copy. Back it up somewhere safe: without it
# you cannot decrypt the secrets Miabi has stored, and the database is
# unrecoverable. The one exception is spec.secrets.gomaConfigEncryptionKey, which
# protects config rendered from the database — rotating it costs a converge.
#
# Fields marked WRITTEN BY MIABI (spec.gateway.configSha, spec.server.dockerGid)
# are derived state. Editing them is how a customized goma.yml gets overwritten.
#
# Extra settings go under spec.server.env — anything Miabi reads that this file
# does not already model (SMTP, OAuth, HTTP_PROXY, …). Variables Miabi sets
# itself are REFUSED there rather than merged.
#
#   miabi stack status    show the running stack against this file
#   miabi setup           converge the stack to this file (safe to re-run)
#   miabi upgrade         roll the stack forward to a newer image
#
`

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

// Existing values are kept so a re-install never rotates live credentials and locks Miabi out of its database.
func (m *Manifest) GenerateSecrets() error {
	for _, f := range []struct {
		dst   *string
		bytes int
	}{
		{&m.Secrets.DBPassword, 24},
		{&m.Secrets.RedisPassword, 24},
		{&m.Secrets.JWTSecret, 32},
		// Length isn't load-bearing, but 32 bytes matches the documented `openssl rand -hex 32`.
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
		// Mixed case and a symbol so it satisfies password policies a bare hex string would fail.
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
