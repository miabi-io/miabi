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
	"path/filepath"
	"testing"
)

func homeAt(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(ConfigPathEnv, "")
	t.Setenv(ManifestPathEnv, "")
	return home
}

func TestUserConfigPath(t *testing.T) {
	home := homeAt(t)

	want := filepath.Join(home, ".miabi", "miabi.yaml")
	if got := UserConfigPath(); got != want {
		t.Fatalf("UserConfigPath() = %q, want %q", got, want)
	}
}

// The overrides win over everything, which is what makes a second stack on one host possible.
func TestManifestPath_EnvWins(t *testing.T) {
	homeAt(t)

	t.Setenv(ConfigPathEnv, "/srv/one/miabi.yaml")
	if got := ManifestPath(); got != "/srv/one/miabi.yaml" {
		t.Fatalf("ManifestPath() = %q, want the %s override", got, ConfigPathEnv)
	}

	t.Setenv(ConfigPathEnv, "")
	t.Setenv(ManifestPathEnv, "/srv/two/miabi.yaml")
	if got := ManifestPath(); got != "/srv/two/miabi.yaml" {
		t.Fatalf("ManifestPath() = %q, want the older %s override", got, ManifestPathEnv)
	}
}

// A location holding an install wins regardless of privilege, so non-sudo commands still find a sudo install.
func TestPickManifestPath(t *testing.T) {
	const etc, user = "/etc/miabi/miabi.yaml", "/home/j/.miabi/miabi.yaml"
	present := func(paths ...string) func(string) bool {
		set := map[string]bool{}
		for _, p := range paths {
			set[p] = true
		}
		return func(p string) bool { return set[p] }
	}

	cases := []struct {
		name   string
		exists func(string) bool
		root   bool
		user   string
		want   string
	}{
		// The case the whole rule exists for: installed with sudo, read back without it.
		{"an /etc install is found by a non-root process", present(etc), false, user, etc},
		{"a per-user install is found by root", present(user), true, user, user},
		{"/etc wins when both exist", present(etc, user), false, user, etc},
		// Nothing installed yet: privilege decides where it goes.
		{"root installs into /etc", present(), true, user, etc},
		{"a user installs into their home", present(), false, user, user},
		{"no home falls back to /etc", present(), false, "", etc},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickManifestPath(etc, c.user, c.exists, c.root); got != c.want {
				t.Fatalf("pickManifestPath = %q, want %q", got, c.want)
			}
		})
	}
}

func TestGatewayConfigFollowsTheManifest(t *testing.T) {
	home := homeAt(t)
	s := &Service{manifestPath: filepath.Join(home, ".miabi", "miabi.yaml")}

	got := s.configPath(&Manifest{})

	want := filepath.Join(home, ".miabi", DefaultGatewayConfigFile)
	if got != want {
		t.Fatalf("gateway config = %q, want %q", got, want)
	}
}
