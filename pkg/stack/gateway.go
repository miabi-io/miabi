// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// configPath is where the gateway config lives AS THIS PROCESS SEES IT — inside the
// installer container, when that is what we are.
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

// EnsureGatewayConfig makes sure the gateway config exists, is valid, and is honest about who owns it:
// absent writes the shipped default and records its digest; a file still matching that digest may be
// replaced by a newer default; a file that differs was edited by the operator and is never touched.
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
		// Either the operator edited it, or it predates ConfigSHA. Do not touch it —
		// but say so, because "my gateway config stopped receiving updates" is exactly
		// the kind of thing nobody notices until it bites.
		s.log("gateway config: using your %s (customized — Miabi will not overwrite it)",
			s.hostPathFor(ctx, path))
	}

	// From here on the path leaves this container and goes to the daemon — as the
	// gateway's bind, and as the validator's. Refuse now if it cannot be mapped.
	host, err := s.requireHostPath(ctx, path)
	if err != nil {
		return err
	}
	m.gatewayHostConfig = host
	s.ensureGeoIP(ctx, m)
	return s.validateGatewayConfig(ctx, m, host)
}

const (
	// DefaultGeoIPFile is the GeoIP database's name beside goma.yml — what the docs
	// tell operators to call the file they supply. Provider-neutral, because Miabi
	// does not choose the provider.
	DefaultGeoIPFile = "country.mmdb"
	// LegacyGeoIPFile is accepted too. It is MaxMind's own download name, so an
	// operator following MaxMind's instructions lands on it without thinking — and
	// earlier Miabi releases downloaded the file under that name.
	LegacyGeoIPFile = "GeoLite2-Country.mmdb"
)

// ensureGeoIP binds a GeoIP database into the gateway when the operator has put one beside goma.yml, so Goma
// can resolve client countries. Miabi does not fetch one deliberately: every database worth having carries a
// license only the operator can accept. MIABI_GEOIP=off skips the lookup without moving the file.
func (s *Service) ensureGeoIP(ctx context.Context, m *Manifest) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MIABI_GEOIP")), "off") {
		s.log("GeoIP: disabled (MIABI_GEOIP=off) — analytics runs without country")
		return
	}
	dir := filepath.Dir(s.manifestPath)
	for _, name := range []string{DefaultGeoIPFile, LegacyGeoIPFile} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		host := s.hostPathFor(ctx, p)
		s.log("GeoIP: using %s", host)
		m.gatewayHostGeoIP = host
		return
	}
	// Not an error — analytics works without countries, and plenty of installs never
	// want them. But name the path anyway: a silent skip is how "the map is empty"
	// turns into a support thread.
	s.log("GeoIP: no database at %s — country data stays off (drop a .mmdb there to enable it)",
		s.hostPathFor(ctx, filepath.Join(dir, DefaultGeoIPFile)))
}

// validateGatewayConfig runs `goma config check` before the gateway is ever started. Worth the extra container:
// the panel's OWN route lives in this file, so a typo does not merely break a feature — it locks the operator
// out of the UI they would fix it in, and the failure would otherwise read as a health-check timeout.
func (s *Service) validateGatewayConfig(ctx context.Context, m *Manifest, host string) error {
	if err := ensureImage(ctx, s.dc, m.Images.Gateway, s.log); err != nil {
		return err
	}
	code, out, err := s.dc.RunOneShot(ctx, docker.RunSpec{
		Name:  "mb-goma-check",
		Image: m.Images.Gateway,
		Cmd:   []string{"config", "check", "--config", "/tmp/goma.yml"},
		// Read-only, and named /tmp/goma.yml rather than its real path: we are checking
		// the file, not booting a gateway around it.
		Binds: []docker.BindMount{{Source: host, Target: "/tmp/goma.yml", ReadOnly: true}},
		// The config interpolates these at parse time; without them the check fails on
		// the file Miabi itself shipped.
		Env:    gatewayConfigEnv(m),
		Labels: map[string]string{docker.LabelPartOf: docker.PartOfMiabi, docker.LabelRole: "config-check"},
	})
	if err != nil {
		// The probe itself broke (no image, no daemon). Do not invent a config error out
		// of it — the gateway's own health gate still backs us up.
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

// requireHostPath resolves the config's host path and refuses to continue if it cannot, which happens exactly
// when the operator forgot to bind-mount the manifest directory. Without it the file lives only inside this
// throwaway container, and Docker's silently-created directory makes the install fail complaining about the config.
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

// hostPath maps a path THIS PROCESS sees to the path the Docker daemon will resolve for a bind mount, and
// reports whether a bind actually covered it. Handing the daemon our own view would point it at a host path
// that may not exist — and Docker silently creates a DIRECTORY there, which Goma then finds instead of goma.yml.
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

// gatewayConfigEnv is the environment the gateway config interpolates, without the
// operator's extras — used by the validator, which only needs the file to parse.
func gatewayConfigEnv(m *Manifest) []string {
	return []string{
		"MIABI_DOMAIN=" + m.Domain,
		"MIABI_ACME_EMAIL=" + m.ACMEEmail,
		"MIABI_REDIS_PASSWORD=" + m.Secrets.RedisPassword,
	}
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
