// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// guardContainerOp refuses destructive operations on protected containers. Before
// platform labels it protected only the self-container (found at runtime via
// /proc) and the node's edge gateway — so `miabi-postgres` and `miabi-redis` were
// stoppable and removable straight from the containers list, taking the whole
// platform down. The runtime self-check cannot cover them: it only ever identifies
// the ONE container this process runs in.
//
// The 409 must also name the component, so an admin learns what they nearly broke.
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

// A compose-owned component gets the extra hint, because "you may not stop this"
// is only half an answer — the admin still needs to know how to restart it.
func TestProtectedMessageMentionsComposeForComposeOwnedComponents(t *testing.T) {
	compose := protectedMessage(docker.PlatformLabels(docker.RolePlatformDB, docker.ManagedByCompose, nil))
	if !strings.Contains(compose, "docker compose up -d") {
		t.Errorf("compose-managed message gives the admin no way forward: %q", compose)
	}
	// Miabi-provisioned infra is managed through its own page, not compose — so the
	// compose hint would be actively misleading there.
	own := protectedMessage(docker.PlatformLabels(docker.RoleNodeGateway, docker.ManagedByMiabi, nil))
	if strings.Contains(own, "docker compose") {
		t.Errorf("miabi-managed component wrongly told to use compose: %q", own)
	}
}

// A user's own container must stay fully controllable — the guard exists to stop
// accidents, not to take the containers page away.
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

// foreignWorkspaceContainer gates whether a platform admin may read a
// container's logs: true (foreign) means blocked.
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
