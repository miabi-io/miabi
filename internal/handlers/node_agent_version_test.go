// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/updatecheck"
)

type fakeAgentReleases struct{ latest string }

func (f fakeAgentReleases) LatestVersion() string { return f.latest }

func TestAnnotateAgentVersions(t *testing.T) {
	h := &NodeHandler{agentRel: fakeAgentReleases{latest: "v0.5.0"}}
	servers := []models.Server{
		{Name: "behind", AccessMode: models.AccessAgent, AgentVersion: "0.4.0"},
		{Name: "ancient", AccessMode: models.AccessAgent, AgentVersion: "0.2.0"},
		{Name: "current", AccessMode: models.AccessAgent, AgentVersion: "0.5.0"},
		{Name: "silent", AccessMode: models.AccessAgent},
		// A socket node runs no agent at all; a badge on it would be nonsense.
		{Name: "socket", AccessMode: models.AccessSocket, AgentVersion: "0.1.0"},
	}
	h.annotateAgentVersions(servers)

	want := map[string]updatecheck.AgentState{
		"behind":  updatecheck.AgentOutdated,
		"ancient": updatecheck.AgentUnsupported,
		"current": updatecheck.AgentCurrent,
		"silent":  updatecheck.AgentUnknown,
		"socket":  updatecheck.AgentUnknown,
	}
	for i := range servers {
		s := &servers[i]
		if got := updatecheck.AgentState(s.AgentState); got != want[s.Name] {
			t.Errorf("%s: state = %q, want %q", s.Name, got, want[s.Name])
		}
	}
	// The version to move to rides along only where there is somewhere to move to.
	if servers[0].AgentLatestVersion != "v0.5.0" || servers[1].AgentLatestVersion != "v0.5.0" {
		t.Error("a node that needs upgrading was not told which version to")
	}
	if servers[2].AgentLatestVersion != "" {
		t.Error("an up-to-date node was handed an upgrade target")
	}
}

// With no release check wired — or one that has never succeeded — the floor is still enforced.
// That half needs no network, and it is the half that matters.
func TestAnnotateAgentVersionsWithoutReleaseData(t *testing.T) {
	h := &NodeHandler{}
	servers := []models.Server{
		{Name: "ancient", AccessMode: models.AccessAgent, AgentVersion: "0.2.0"},
		{Name: "fine", AccessMode: models.AccessAgent, AgentVersion: "0.5.0"},
	}
	h.annotateAgentVersions(servers)

	if updatecheck.AgentState(servers[0].AgentState) != updatecheck.AgentUnsupported {
		t.Errorf("offline install lost the unsupported verdict: %q", servers[0].AgentState)
	}
	if updatecheck.AgentState(servers[1].AgentState) != updatecheck.AgentCurrent {
		t.Errorf("a supported agent was flagged with no release data: %q", servers[1].AgentState)
	}
}
