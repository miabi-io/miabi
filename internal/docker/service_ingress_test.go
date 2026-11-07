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

package docker

import (
	"testing"

	"github.com/moby/moby/api/types/swarm"
)

const (
	ingressNet = "miabi-ingress"
	ingressID  = "b7c1e4f09a2d"
)

func targets(nets []swarm.NetworkAttachmentConfig) []string {
	out := make([]string, 0, len(nets))
	for _, n := range nets {
		out = append(out, n.Target)
	}
	return out
}

// Runs on every route save, and re-asserting the current state would restart tasks for nothing.
func TestIngressAttachmentsNoChangeWhenAlreadyCorrect(t *testing.T) {
	on := []swarm.NetworkAttachmentConfig{
		{Target: "mb-ws-1-overlay", Aliases: []string{"web"}},
		{Target: ingressNet, Aliases: []string{"mb-app-abc-1"}},
	}
	if _, changed := ingressAttachments(on, ingressNet, ingressID, "mb-app-abc-1", true); changed {
		t.Error("attaching an already-attached service reported a change")
	}

	off := []swarm.NetworkAttachmentConfig{{Target: "mb-ws-1-overlay"}}
	if _, changed := ingressAttachments(off, ingressNet, ingressID, "mb-app-abc-1", false); changed {
		t.Error("detaching an already-detached service reported a change")
	}
}

// Matching on the name alone would add a duplicate attachment on every route change.
func TestIngressAttachmentsMatchesByID(t *testing.T) {
	nets := []swarm.NetworkAttachmentConfig{
		{Target: "mb-ws-1-overlay"},
		{Target: ingressID, Aliases: []string{"mb-app-abc-1"}},
	}
	if _, changed := ingressAttachments(nets, ingressNet, ingressID, "mb-app-abc-1", true); changed {
		t.Error("an attachment recorded by id was not recognised, so it would be added twice")
	}
	next, changed := ingressAttachments(nets, ingressNet, ingressID, "mb-app-abc-1", false)
	if !changed {
		t.Fatal("an attachment recorded by id was not removed")
	}
	if got := targets(next); len(got) != 1 || got[0] != "mb-ws-1-overlay" {
		t.Errorf("networks = %v, want the workspace overlay alone", got)
	}
}

// Tenant-scoped names stay on the workspace network, where they cannot collide across workspaces.
func TestIngressAttachmentsAttachCarriesOnlyTheUpstreamAlias(t *testing.T) {
	nets := []swarm.NetworkAttachmentConfig{{Target: "mb-ws-1-overlay", Aliases: []string{"web", "mb-app-abc-1"}}}

	next, changed := ingressAttachments(nets, ingressNet, ingressID, "mb-app-abc-1", true)
	if !changed {
		t.Fatal("attaching an unrouted service reported no change")
	}
	if got := targets(next); len(got) != 2 || got[1] != ingressNet {
		t.Fatalf("networks = %v, want the ingress overlay appended", got)
	}
	if a := next[1].Aliases; len(a) != 1 || a[0] != "mb-app-abc-1" {
		t.Errorf("ingress aliases = %v, want only the upstream alias", a)
	}
	// The workspace attachment is left exactly as it was.
	if a := next[0].Aliases; len(a) != 2 {
		t.Errorf("workspace aliases = %v, want them untouched", a)
	}
}

// Detaching leaves every other network in place; only the ingress overlay goes.
func TestIngressAttachmentsDetachKeepsTheRest(t *testing.T) {
	nets := []swarm.NetworkAttachmentConfig{
		{Target: "mb-ws-1-overlay"},
		{Target: ingressNet, Aliases: []string{"mb-app-abc-1"}},
		{Target: "mb-stack-7"},
	}
	next, changed := ingressAttachments(nets, ingressNet, ingressID, "mb-app-abc-1", false)
	if !changed {
		t.Fatal("detaching a routed service reported no change")
	}
	got := targets(next)
	if len(got) != 2 || got[0] != "mb-ws-1-overlay" || got[1] != "mb-stack-7" {
		t.Errorf("networks = %v, want the workspace and stack networks kept", got)
	}
}

// An app nobody can reach from outside stays off the shared overlay.
func TestBuildSwarmServiceSpecUnroutedStaysOffTheOverlay(t *testing.T) {
	spec := buildSwarmServiceSpec(ServiceSpec{
		Name:           "mb-app-abc-1",
		Image:          "nginx",
		Networks:       []string{"mb-ws-1-overlay"},
		NetworkAliases: []string{"mb-app-abc-1", "web"},
		IngressNetwork: "", // no route
	})
	for _, n := range spec.TaskTemplate.Networks {
		if n.Target == ingressNet {
			t.Fatalf("an unrouted service joined %s anyway", ingressNet)
		}
	}
}
