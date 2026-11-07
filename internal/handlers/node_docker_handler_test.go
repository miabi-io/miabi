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

package handlers

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// The 409 must name the component an admin nearly broke.
func TestProtectedMessageNamesTheComponent(t *testing.T) {
	cases := []struct {
		name    string
		labels  map[string]string
		wantSub string
	}{
		{
			"platform database",
			docker.PlatformLabels(docker.RolePlatformDB, docker.ManagedByCompose, nil),
			"Miabi's own database",
		},
		{
			"control plane",
			docker.PlatformLabels(docker.RoleControlPlane, docker.ManagedByCompose, nil),
			"the Miabi control plane",
		},
		{
			"node agent",
			docker.PlatformLabels(docker.RoleAgent, docker.ManagedByExternal, nil),
			"this node's Miabi agent",
		},
		{
			// Protected but with an unknown//future role: still refused, still readable.
			"unknown role falls back",
			map[string]string{docker.LabelProtected: "true", docker.LabelRole: "some-future-thing"},
			"part of the Miabi platform",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !docker.IsProtected(c.labels) {
				t.Fatal("precondition: labels are not protected")
			}
			msg := protectedMessage(c.labels)
			if !strings.Contains(msg, c.wantSub) {
				t.Errorf("message = %q, want it to contain %q", msg, c.wantSub)
			}
		})
	}
}

// A compose-owned component also needs to say how to restart it.
func TestProtectedMessageMentionsComposeForComposeOwnedComponents(t *testing.T) {
	compose := protectedMessage(docker.PlatformLabels(docker.RolePlatformDB, docker.ManagedByCompose, nil))
	if !strings.Contains(compose, "docker compose up -d") {
		t.Errorf("compose-managed message gives the admin no way forward: %q", compose)
	}
	// Miabi-provisioned infra is managed through its own page, so the compose hint would mislead.
	own := protectedMessage(docker.PlatformLabels(docker.RoleNodeGateway, docker.ManagedByMiabi, nil))
	if strings.Contains(own, "docker compose") {
		t.Errorf("miabi-managed component wrongly told to use compose: %q", own)
	}
}

// The guard exists to stop accidents, not to take the containers page away.
func TestUserContainersAreNotProtected(t *testing.T) {
	for _, labels := range []map[string]string{
		{},
		nil,
		{"com.docker.compose.project": "blog"},
		{docker.LabelApp: "7", docker.LabelWorkspace: "1"}, // managed, but not protected
	} {
		if docker.IsProtected(labels) {
			t.Errorf("IsProtected(%v) = true; a user could not stop their own container", labels)
		}
	}
}

func TestForeignWorkspaceContainer(t *testing.T) {
	member := map[uint]bool{1: true, 2: true}
	cases := []struct {
		name        string
		labels      map[string]string
		wantForeign bool
	}{
		{"no labels (raw container)", map[string]string{}, false},
		{"non-platform labels", map[string]string{"com.example": "x"}, false},
		{"infra gateway never foreign", map[string]string{docker.LabelRole: "node-gateway", docker.LabelWorkspace: "999"}, false},
		{"own workspace app", map[string]string{docker.LabelApp: "10", docker.LabelWorkspace: "1"}, false},
		{"other workspace app is foreign", map[string]string{docker.LabelApp: "10", docker.LabelWorkspace: "3"}, true},
		{"other workspace database is foreign", map[string]string{docker.LabelDatabase: "4", docker.LabelWorkspace: "3"}, true},
		{"app with no workspace label not foreign", map[string]string{docker.LabelApp: "10"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := foreignWorkspaceContainer(c.labels, member); got != c.wantForeign {
				t.Errorf("foreignWorkspaceContainer(%v) = %v, want %v", c.labels, got, c.wantForeign)
			}
		})
	}
}
