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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/miabi-io/miabi/pkg/stack/docker"
	"github.com/miabi-io/miabi/pkg/stack/selfcontainer"
)

// DefaultGatewayConfigFile is the gateway config's name beside miabi.yaml.
const DefaultGatewayConfigFile = "goma.yml"

// The path as this process sees it, i.e. inside the installer container when there.
func (s *Service) configPath(m *Manifest) string {
	name := strings.TrimSpace(m.Gateway.Config)
	if name == "" {
		name = DefaultGatewayConfigFile
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(filepath.Dir(s.manifestPath), name)
}

// A file that no longer matches the recorded default digest was edited by the operator and is never touched.
func (s *Service) EnsureGatewayConfig(ctx context.Context, m *Manifest) error {
	path := s.configPath(m)
	want := gomaConfig
	wantSHA := sha256hex(want)

	cur, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		s.log("gateway config: writing the default to %s", s.hostPathFor(ctx, path))
		if err := writeFile(path, want); err != nil {
			return err
		}
		m.Gateway.ConfigSHA = wantSHA

	case err != nil:
		return fmt.Errorf("read %s: %w", path, err)

	case sha256hex(cur) == m.Gateway.ConfigSHA && m.Gateway.ConfigSHA != "":
		// Untouched since Miabi wrote it. Safe to refresh with this release's default.
		if wantSHA != m.Gateway.ConfigSHA {
			s.log("gateway config: updating %s to this release's default (it was unmodified)",
				s.hostPathFor(ctx, path))
			if err := writeFile(path, want); err != nil {
				return err
			}
			m.Gateway.ConfigSHA = wantSHA
		}

	default:
		// Say so, because a gateway config that stopped receiving updates otherwise goes unnoticed.
		s.log("gateway config: using your %s (customized — Miabi will not overwrite it)",
			s.hostPathFor(ctx, path))
	}

	// The path is about to go to the daemon, so refuse now if it cannot be mapped to the host.
	host, err := s.requireHostPath(ctx, path)
	if err != nil {
		return err
	}
	m.gatewayHostConfig = host
	s.ensureGeoIP(ctx, m)
	return s.validateGatewayConfig(ctx, m, host)
}

const (
	// Mount the directory, not the file: a single-file bind keeps the old inode and misses rename-based updates.
	GeoIPDir = "geoip"
	// Provider-neutral, because Miabi does not choose the provider.
	DefaultGeoIPFile = "country.mmdb"
	// MaxMind's own download name, so operators following MaxMind or geoipupdate land on it.
	LegacyGeoIPFile = "GeoLite2-Country.mmdb"
)

var geoIPFiles = []string{DefaultGeoIPFile, LegacyGeoIPFile}

// Bound even while empty so a later database is picked up; Miabi fetches none since only operators can accept licenses.
func (s *Service) ensureGeoIP(ctx context.Context, m *Manifest) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MIABI_GEOIP")), "off") {
		s.log("GeoIP: disabled (MIABI_GEOIP=off) — analytics runs without country")
		return
	}
	base := filepath.Dir(s.manifestPath)
	dir := filepath.Join(base, GeoIPDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.log("GeoIP: cannot create %s (%v) — country data stays off", dir, err)
		return
	}
	s.moveLegacyGeoIP(base, dir)
	host := s.hostPathFor(ctx, dir)
	m.gatewayHostGeoIP = host
	for _, name := range geoIPFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			s.log("GeoIP: using %s", filepath.Join(host, name))
			return
		}
	}
	// A silent skip turns "the map is empty" into a support thread.
	s.log("GeoIP: no database in %s — drop a %s there to enable country data (no restart needed)", host, DefaultGeoIPFile)
}

// A database already in the directory wins: the operator put it there on purpose.
func (s *Service) moveLegacyGeoIP(base, dir string) {
	for _, name := range geoIPFiles {
		from, to := filepath.Join(base, name), filepath.Join(dir, name)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if _, err := os.Stat(to); err == nil {
			s.log("GeoIP: %s is no longer read — %s takes its place; delete the old file", from, to)
			continue
		}
		if err := os.Rename(from, to); err != nil {
			s.log("GeoIP: could not move %s to %s (%v) — move it by hand", from, to, err)
			continue
		}
		s.log("GeoIP: moved %s to %s", from, to)
	}
}

// The panel's own route lives in this file, so a typo would lock the operator out of the UI.
func (s *Service) validateGatewayConfig(ctx context.Context, m *Manifest, host string) error {
	if err := ensureImage(ctx, s.dc, m.Images.Gateway, s.log); err != nil {
		return err
	}
	code, out, err := s.dc.RunOneShot(ctx, docker.RunSpec{
		Name:  "mb-goma-check",
		Image: m.Images.Gateway,
		Cmd:   []string{"config", "check", "--config", "/tmp/goma.yml"},
		Binds: []docker.BindMount{{Source: host, Target: "/tmp/goma.yml", ReadOnly: true}},
		// Without these the check fails on the file Miabi itself shipped.
		Env:    gatewayConfigEnv(m),
		Labels: map[string]string{docker.LabelPartOf: docker.PartOfMiabi, docker.LabelRole: "config-check"},
	})
	if err != nil {
		// A broken probe is not a config error; the gateway's health gate still backs us up.
		s.log("gateway config: could not validate it (%v) — continuing", err)
		return nil
	}
	if code != 0 {
		return fmt.Errorf("the gateway config at %s is not valid — Goma refused it, so the "+
			"gateway would never start. Fix the file and re-run:\n\n%s",
			host, strings.TrimSpace(out))
	}
	return nil
}

// Fails when the manifest dir isn't bind-mounted; otherwise Docker silently creates a directory in its place.
func (s *Service) requireHostPath(ctx context.Context, path string) (string, error) {
	host, mapped := s.hostPath(ctx, path)
	if mapped || selfcontainer.Detect() == "" {
		return host, nil
	}
	return "", fmt.Errorf("%s is not bind-mounted from the host, so the gateway could never "+
		"read it — Docker would create an empty directory there instead.\n\n"+
		"  Add the mount and re-run:\n\n"+
		"    docker run --rm -it \\\n"+
		"      -v /var/run/docker.sock:/var/run/docker.sock \\\n"+
		"      -v %s:%s \\\n"+
		"      <image> install …",
		filepath.Dir(path), filepath.Dir(path), filepath.Dir(path))
}

// hostPathFor is requireHostPath's forgiving twin, for logging: it never fails.
func (s *Service) hostPathFor(ctx context.Context, path string) string {
	host, _ := s.hostPath(ctx, path)
	return host
}

// Docker silently creates a directory at a missing bind source, which Goma then finds instead of goma.yml.
func (s *Service) hostPath(ctx context.Context, path string) (host string, mapped bool) {
	id := selfcontainer.Detect()
	if id == "" {
		return path, true // running as a plain binary: our view IS the host's
	}
	cfg, err := s.dc.InspectContainerConfig(ctx, id)
	if err != nil {
		return path, false
	}
	// Longest destination wins, so a nested mount beats its parent.
	best, src := "", ""
	for _, mnt := range cfg.Mounts {
		if mnt.Type != "bind" || mnt.Source == "" {
			continue
		}
		if path == mnt.Destination || strings.HasPrefix(path, mnt.Destination+"/") {
			if len(mnt.Destination) > len(best) {
				best, src = mnt.Destination, mnt.Source
			}
		}
	}
	if best == "" {
		return path, false
	}
	return filepath.Join(src, strings.TrimPrefix(path, best)), true
}

func gatewayConfigEnv(m *Manifest) []string {
	env := []string{
		"MIABI_DOMAIN=" + m.Domain,
		"MIABI_ACME_EMAIL=" + m.ACMEEmail,
		"MIABI_REDIS_PASSWORD=" + m.Secrets.RedisPassword,
	}
	// Through env rather than goma.yml, which is the operator's file once edited and is never rewritten.
	if proxies := m.Install.GatewayTrustedProxies; len(proxies) > 0 {
		env = append(env,
			"GOMA_PROXY_ENABLED=true",
			"GOMA_PROXY_TRUSTED_PROXIES="+strings.Join(proxies, ","),
		)
	}
	// Env rather than goma.yml: an older Goma ignores an unknown variable but refuses an unknown key.
	if m.Install.GatewayCountryHeader {
		env = append(env, "GOMA_GEOIP_COUNTRY_HEADER=true")
	}
	return env
}

func writeFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
