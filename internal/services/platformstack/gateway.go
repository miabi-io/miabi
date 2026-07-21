// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformstack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/selfcontainer"
)

// DefaultGatewayConfigFile is the gateway config's name beside stack.yaml.
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

// EnsureGatewayConfig makes sure the gateway config exists, is valid, and is honest
// about whether the operator owns it.
//
// The policy, and why it is not simply "always write the default":
//
//   - absent            → write the shipped default, record its digest.
//   - matches digest    → nobody touched it. A newer release's default REPLACES it,
//     so installs that never customized still receive upstream fixes.
//   - differs           → the operator edited it. Never touch it again.
//
// Without the digest you must choose between "customization is impossible" (always
// overwrite — which is what copying into a volume did) and "every install is frozen
// on the config it shipped with" (never overwrite). Neither tells the operator which
// one they are living in.
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
	// DefaultGeoIPFile is the GeoIP database's name beside goma.yml. Goma reads it
	// at /etc/goma/GeoLite2-Country.mmdb (its GOMA_GEOIP_DB default).
	DefaultGeoIPFile = "GeoLite2-Country.mmdb"
	// DefaultGeoIPURL is the auto-updated GeoLite2-Country mirror. MaxMind's own
	// endpoint needs a license key; this mirror does not. Override MIABI_GEOIP_URL.
	DefaultGeoIPURL = "https://github.com/P3TERX/GeoLite.mmdb/releases/download/2026.07.16/GeoLite2-Country.mmdb"
)

// ensureGeoIP provisions the GeoIP database beside goma.yml so Goma can resolve
// client countries for workspace analytics. Best-effort — it never fails the
// install; on any problem, country enrichment simply stays off. Controlled by:
//
//	MIABI_GEOIP=off       skip entirely
//	MIABI_GEOIP_URL=<url>  download source (default: the P3TERX GeoLite mirror)
//
// An existing file — a prior install, or one the operator supplied (air-gapped,
// or a licensed MaxMind/IP2Location .mmdb) — is used as-is and never re-downloaded.
func (s *Service) ensureGeoIP(ctx context.Context, m *Manifest) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MIABI_GEOIP")), "off") {
		s.log("GeoIP: disabled (MIABI_GEOIP=off) — analytics runs without country")
		return
	}
	path := filepath.Join(filepath.Dir(s.manifestPath), DefaultGeoIPFile)
	if _, err := os.Stat(path); err == nil {
		m.gatewayHostGeoIP = s.hostPathFor(ctx, path) // already present — use it
		return
	}
	url := strings.TrimSpace(os.Getenv("MIABI_GEOIP_URL"))
	if url == "" {
		url = DefaultGeoIPURL
	}
	s.log("GeoIP: downloading %s", DefaultGeoIPFile)
	if err := downloadFile(ctx, url, path); err != nil {
		s.log("GeoIP: download failed (%v) — country enrichment stays off; set MIABI_GEOIP_URL, "+
			"drop a .mmdb at %s, or set MIABI_GEOIP=off to silence this", err, s.hostPathFor(ctx, path))
		return
	}
	s.log("GeoIP: ready at %s", s.hostPathFor(ctx, path))
	m.gatewayHostGeoIP = s.hostPathFor(ctx, path)
}

// downloadFile fetches url into dst atomically (temp file + rename, so the
// gateway never reads a half-written database), with a bounded timeout so a slow
// mirror can't hang the install.
func downloadFile(ctx context.Context, url, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "miabi-installer")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, dst)
}

// validateGatewayConfig runs `goma config check` before the gateway is ever started.
//
// Worth the extra container: the panel's OWN route lives in this file, so a typo does
// not merely break a feature — it locks the operator out of the UI they would use to
// fix it. And without this the failure arrives as "miabi-gateway did not become
// healthy within 1m30s", which says nothing about the missing colon on line 34.
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

// requireHostPath resolves the config's host path, and refuses to continue if it
// cannot — which happens exactly when the operator forgot to bind-mount the manifest
// directory.
//
// Without that mount the file we just wrote lives only inside this throwaway
// container. Handing the daemon our own view of the path makes it bind a host path
// that does not exist, and Docker's answer to a missing bind source is to silently
// create a DIRECTORY. Goma then finds a folder where goma.yml should be and refuses
// it — so the install does fail, but it fails complaining about the CONFIG, which is
// not the problem at all. Name the real one.
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

// hostPath maps a path THIS PROCESS sees to the path the Docker daemon will
// resolve for a bind mount. mapped reports whether a bind actually covered it.
//
// This is the crux of bind-mounting from inside the installer container. Our
// /etc/miabi is a bind of some host directory — /etc/miabi by default, but anything
// under MIABI_ETC. Handing the daemon our own view of the path would point it at a
// path on the HOST that may not exist — and Docker's response to a missing bind
// source is to silently create a DIRECTORY. Goma would then find a folder where
// goma.yml should be and fail for a reason that looks nothing like the cause.
//
// So: ask Docker what our own container's mounts are, and translate.
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
