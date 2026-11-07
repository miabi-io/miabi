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

package updatecheck

// 0.4.0 is where agents began forwarding gateway events for analytics; a badge, never a gate.
const MinAgentVersion = "0.4.0"

// AgentState is how a node's agent stands against this control plane.
type AgentState string

const (
	// Renders as no badge: absence of evidence must not read as "out of date".
	AgentUnknown AgentState = ""
	// AgentCurrent is supported and at the newest release we know of.
	AgentCurrent AgentState = "current"
	// AgentOutdated is supported, but a newer release exists.
	AgentOutdated AgentState = "outdated"
	// Determined locally, so it holds on air-gapped installs where the release check never succeeded.
	AgentUnsupported AgentState = "unsupported"
)

// An unparseable version is not called unsupported; guessing wrong paints a working fleet red.
func AgentSupported(current string) bool {
	if normalize(current) == "" {
		return true
	}
	return !IsNewer(current, MinAgentVersion)
}

// latest may be empty: the unsupported verdict needs no network.
func ClassifyAgent(current, latest string) AgentState {
	if normalize(current) == "" {
		return AgentUnknown
	}
	if !AgentSupported(current) {
		return AgentUnsupported
	}
	if latest != "" && IsNewer(current, latest) {
		return AgentOutdated
	}
	return AgentCurrent
}

// One indexed row read per request; an in-memory cache would go stale against the cron that writes it.
func (s *Service) LatestVersion() string {
	st, err := s.Status()
	if err != nil {
		return ""
	}
	return st.LatestVersion
}
