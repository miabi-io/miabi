// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package dockerimport

import (
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

func TestSplitImageTag(t *testing.T) {
	cases := []struct {
		ref, image, tag string
	}{
		{"nginx", "nginx", ""},
		{"nginx:1.25", "nginx", "1.25"},
		{"ghcr.io/org/app:v2", "ghcr.io/org/app", "v2"},
		{"registry:5000/app", "registry:5000/app", ""}, // ":" before last "/" is a port, not a tag
		{"registry:5000/app:dev", "registry:5000/app", "dev"},
		{"repo@sha256:abc", "repo@sha256:abc", ""}, // digest-pinned
	}
	for _, c := range cases {
		gotImage, gotTag := splitImageTag(c.ref)
		if gotImage != c.image || gotTag != c.tag {
			t.Errorf("splitImageTag(%q) = (%q, %q), want (%q, %q)", c.ref, gotImage, gotTag, c.image, c.tag)
		}
	}
}

func TestIsSecretKey(t *testing.T) {
	secret := []string{"DB_PASSWORD", "API_TOKEN", "JWT_SECRET", "AWS_ACCESS_KEY", "STRIPE_APIKEY", "TLS_PRIVATE_KEY", "SIGNING_KEY", "KEY"}
	plain := []string{"PORT", "NODE_ENV", "HOSTNAME", "DEBUG", "KEYBOARD_LAYOUT"}
	for _, k := range secret {
		if !isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = false, want true", k)
		}
	}
	for _, k := range plain {
		if isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = true, want false", k)
		}
	}
}

func TestIsMiabiName(t *testing.T) {
	managed := []string{"mb-vol-1-data", "mb-ws3-abc", "miabi", "mb-stack-foo"}
	external := []string{"my_data", "postgres_data", "app-network", "redis"}
	for _, n := range managed {
		if !isMiabiName(n) {
			t.Errorf("isMiabiName(%q) = false, want true", n)
		}
	}
	for _, n := range external {
		if isMiabiName(n) {
			t.Errorf("isMiabiName(%q) = true, want false", n)
		}
	}
}

func TestIsManaged(t *testing.T) {
	if !isManaged(map[string]string{"io.miabi.app": "1"}) {
		t.Error("expected io.miabi.app to be managed")
	}
	if isManaged(map[string]string{"com.docker.compose.project": "blog"}) {
		t.Error("expected a compose-only container to be unmanaged")
	}
}

// Unlabeled compose-deployed platform containers were offered for import and could be recreated by the deploy worker.
func TestPlatformStackIsNeverImportable(t *testing.T) {
	platform := docker.PlatformLabels(docker.RolePlatformDB, docker.ManagedByCompose, nil)

	cases := []struct {
		name   string
		labels map[string]string
		dName  string
		want   bool // true = must NOT be offered for import
	}{
		// Labeled — the fix.
		{"labeled control plane", docker.PlatformLabels(docker.RoleControlPlane, docker.ManagedByCompose, nil), "miabi", true},
		{"labeled platform postgres", platform, "miabi-postgres", true},
		{"labeled agent", docker.PlatformLabels(docker.RoleAgent, docker.ManagedByExternal, nil), "miabi-agent", true},

		// A stack installed before platform labels is covered only by the name shield.
		{"unlabeled platform postgres", nil, "miabi-postgres", true},
		{"unlabeled platform redis", nil, "miabi-redis", true},
		{"unlabeled control plane", nil, "miabi", true},
		{"unlabeled central gateway", nil, "miabi-gateway", true},
		{"container name keeps docker's leading slash", nil, "/miabi-postgres", true},

		// Compose prefixes volume names with the project, so platform volumes surface as miabi_pgdata.
		{"compose-prefixed platform volume", nil, "miabi_pgdata", true},
		{"miabi-managed resource", map[string]string{docker.LabelApp: "7"}, "mb-app-x", true},

		// A user's own containers must still be importable — the whole point of the page.
		{"user postgres", nil, "postgres", false},
		{"user app", nil, "blog-web-1", false},
		{"compose-managed user stack", map[string]string{"com.docker.compose.project": "blog"}, "blog_db_1", false},
		{"user volume", nil, "postgres_data", false},
		{"unrelated name with the same prefix", nil, "miabify", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := notImportable(c.labels, c.dName); got != c.want {
				t.Errorf("notImportable(%v, %q) = %v, want %v", c.labels, c.dName, got, c.want)
			}
		})
	}
}

// Compose pins container_name only on the gateway, so other platform names depend on the install directory.
func TestUnlabeledStackIsShieldedByItsComposeProject(t *testing.T) {
	const project = "miabi" // the control plane's own com.docker.compose.project

	inStack := func(service string) map[string]string {
		return map[string]string{
			composeProjectLabel:          project,
			"com.docker.compose.service": service,
		}
	}

	// Real generated names from `docker compose -p miabi up`, with NO io.miabi.* labels.
	for _, c := range []struct {
		name   string
		labels map[string]string
	}{
		{"miabi-miabi-postgres-1", inStack("miabi-postgres")},
		{"miabi-miabi-redis-1", inStack("miabi-redis")},
		{"miabi-miabi-1", inStack("miabi")},
		{"miabi-gateway", inStack("gateway")},
	} {
		// The name shield misses generated names, so assert the combination holds.
		skipped := notImportable(c.labels, c.name) || inPlatformProject(c.labels, project)
		if !skipped {
			t.Errorf("%s (unlabeled, project=%s) would be offered for import", c.name, project)
		}
	}

	// A user's OWN compose stack is a different project and stays importable.
	user := map[string]string{composeProjectLabel: "blog", "com.docker.compose.service": "db"}
	if notImportable(user, "blog-db-1") || inPlatformProject(user, project) {
		t.Error("a user's compose stack was shielded — the import page would be empty")
	}

	// An empty project must disable the shield, not match everything on the node.
	if inPlatformProject(user, "") {
		t.Error("an empty project matched — discovery would shield everything")
	}
}
