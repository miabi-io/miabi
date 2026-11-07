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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Binding a missing host path would make Docker create it as root.
func TestGatewaySpecBindsGeoIPOnlyWhenProvisioned(t *testing.T) {
	m := Defaults("miabi/miabi:1.4.0")

	bind := func() (source string, readOnly, found bool) {
		for _, b := range gatewaySpec(m, ContainerGateway, m.Images.Gateway).Binds {
			if b.Target == gomaGeoIPDir {
				return b.Source, b.ReadOnly, true
			}
		}
		return "", false, false
	}

	if _, _, found := bind(); found {
		t.Fatal("GeoIP database bound when none was provisioned")
	}

	const host = "/etc/miabi/geoip"
	m.gatewayHostGeoIP = host
	source, readOnly, found := bind()
	if !found {
		t.Fatal("GeoIP database not bound after provisioning")
	}
	if source != host || !readOnly {
		t.Fatalf("GeoIP bind wrong: source=%q readOnly=%v, want %q read-only", source, readOnly, host)
	}
}

func geoIPService(t *testing.T) (s *Service, base string, logs *[]string) {
	t.Helper()
	base = t.TempDir()
	var lines []string
	s = &Service{manifestPath: filepath.Join(base, "miabi.yaml"), log: func(f string, a ...any) {
		lines = append(lines, fmt.Sprintf(f, a...))
	}}
	return s, base, &lines
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Bound even while empty: a database dropped in later reaches the running gateway with no converge.
func TestEnsureGeoIPBindsTheDirectoryEvenWhenEmpty(t *testing.T) {
	s, base, logs := geoIPService(t)
	m := Defaults("miabi/miabi:1.4.0")
	s.ensureGeoIP(context.Background(), m)

	dir := filepath.Join(base, GeoIPDir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("GeoIP directory not created: %v", err)
	}
	if m.gatewayHostGeoIP != dir {
		t.Fatalf("gatewayHostGeoIP = %q, want %q", m.gatewayHostGeoIP, dir)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "no database in "+dir) {
		t.Errorf("an empty directory was not reported: %v", *logs)
	}
}

func TestEnsureGeoIPMovesTheFileFromItsOldPlace(t *testing.T) {
	s, base, _ := geoIPService(t)
	mustWrite(t, filepath.Join(base, DefaultGeoIPFile), "db")
	s.ensureGeoIP(context.Background(), Defaults("miabi/miabi:1.4.0"))

	if _, err := os.Stat(filepath.Join(base, DefaultGeoIPFile)); !os.IsNotExist(err) {
		t.Error("the old file was left beside goma.yml")
	}
	if got := readFile(t, filepath.Join(base, GeoIPDir, DefaultGeoIPFile)); got != "db" {
		t.Errorf("moved database = %q, want the original", got)
	}
}

func TestEnsureGeoIPNeverOverwritesTheDirectorysDatabase(t *testing.T) {
	s, base, logs := geoIPService(t)
	if err := os.MkdirAll(filepath.Join(base, GeoIPDir), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(base, GeoIPDir, DefaultGeoIPFile), "new")
	mustWrite(t, filepath.Join(base, DefaultGeoIPFile), "old")
	s.ensureGeoIP(context.Background(), Defaults("miabi/miabi:1.4.0"))

	if got := readFile(t, filepath.Join(base, GeoIPDir, DefaultGeoIPFile)); got != "new" {
		t.Errorf("the directory's database was replaced by the old file (%q)", got)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "no longer read") {
		t.Errorf("the stale old file was not reported: %v", *logs)
	}
}

func TestEnsureGeoIPOffMountsNothing(t *testing.T) {
	t.Setenv("MIABI_GEOIP", "off")
	s, base, _ := geoIPService(t)
	m := Defaults("miabi/miabi:1.4.0")
	s.ensureGeoIP(context.Background(), m)
	if m.gatewayHostGeoIP != "" {
		t.Errorf("GeoIP bound while MIABI_GEOIP=off: %q", m.gatewayHostGeoIP)
	}
	if _, err := os.Stat(filepath.Join(base, GeoIPDir)); !os.IsNotExist(err) {
		t.Error("GeoIP directory created while MIABI_GEOIP=off")
	}
}
