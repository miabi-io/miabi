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

package route

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/proxy"
)

func names(mws []models.Middleware) []string {
	out := make([]string, 0, len(mws))
	for _, m := range mws {
		out = append(out, m.Name)
	}
	return out
}

// An edge bundle must carry only middlewares its own routes name; others ship decrypted secrets.
func TestReferencedMiddlewaresKeepsOnlyWhatTheRoutesName(t *testing.T) {
	mws := []models.Middleware{
		{ID: 1, WorkspaceID: 1, Name: "auth"},
		{ID: 2, WorkspaceID: 1, Name: "ratelimit"},
		{ID: 3, WorkspaceID: 2, Name: "auth"},       // another tenant's, same name
		{ID: 4, WorkspaceID: 2, Name: "forwardsso"}, // another tenant's secrets
		{ID: 5, WorkspaceID: 3, Name: "cors"},
	}
	routes := []proxy.RenderedRoute{
		{WorkspaceID: 1, Name: "web", Middlewares: []string{"auth"}},
		{WorkspaceID: 1, Name: "api", Middlewares: []string{"ratelimit"}},
	}

	got := referencedMiddlewares(mws, routes)
	if len(got) != 2 {
		t.Fatalf("kept %v, want only workspace 1's auth and ratelimit", names(got))
	}
	for _, m := range got {
		if m.WorkspaceID != 1 {
			t.Errorf("workspace %d's %q leaked into the bundle", m.WorkspaceID, m.Name)
		}
	}
}

// Two workspaces may each define "auth", so matching on name alone would hand one tenant the other's secrets.
func TestReferencedMiddlewaresMatchesPerWorkspace(t *testing.T) {
	mws := []models.Middleware{
		{ID: 1, WorkspaceID: 1, Name: "auth"},
		{ID: 2, WorkspaceID: 2, Name: "auth"},
	}
	got := referencedMiddlewares(mws, []proxy.RenderedRoute{
		{WorkspaceID: 2, Middlewares: []string{"auth"}},
	})
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("kept %v, want only workspace 2's auth (id 2)", got)
	}
}

// Goma rejects a config referencing an undefined middleware, so disabled routes' middlewares must ship too.
func TestReferencedMiddlewaresIncludesDisabledRoutes(t *testing.T) {
	mws := []models.Middleware{{ID: 1, WorkspaceID: 1, Name: "auth"}}
	got := referencedMiddlewares(mws, []proxy.RenderedRoute{
		{WorkspaceID: 1, Middlewares: []string{"auth"}, Disabled: true},
	})
	if len(got) != 1 {
		t.Fatalf("kept %v, want the disabled route's middleware kept", names(got))
	}
}

// A node serving nothing ships nothing — the case that leaked the most before.
func TestReferencedMiddlewaresEmptyWhenNoRoutes(t *testing.T) {
	mws := []models.Middleware{
		{ID: 1, WorkspaceID: 1, Name: "auth"},
		{ID: 2, WorkspaceID: 2, Name: "forwardsso"},
	}
	if got := referencedMiddlewares(mws, nil); len(got) != 0 {
		t.Errorf("a node with no routes received %v", names(got))
	}
}
