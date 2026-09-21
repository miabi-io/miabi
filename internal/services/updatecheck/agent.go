// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package updatecheck

// MinAgentVersion is the oldest node agent this control plane supports — the counterpart to
// docker.MinEngineVersion, and the threshold the Nodes page paints red.
//
// It is 0.4.0 because that is where the agent began forwarding its node gateway's request events to
// the manager. Below it a node connects, deploys and reports healthy while contributing nothing to
// Workspace Analytics, which is the kind of gap an operator finds months later in a graph.
//
// Bump it when a handshake or protocol change makes an older agent genuinely wrong, not merely old.
// It is a badge, never a gate: refusing an old agent would strand a fleet the moment Miabi upgraded.
const MinAgentVersion = "0.4.0"

// AgentState is how a node's agent stands against this control plane.
type AgentState string

const (
	// AgentUnknown covers a node that has never reported a version and one whose version is not a
	// release at all (a `dev` build, a commit sha). It renders as no badge: absence of evidence must
	// not read as "out of date", or a broken checker masquerades as a verdict.
	AgentUnknown AgentState = ""
	// AgentCurrent is supported and at the newest release we know of.
	AgentCurrent AgentState = "current"
	// AgentOutdated is supported, but a newer release exists.
	AgentOutdated AgentState = "outdated"
	// AgentUnsupported is below MinAgentVersion. Determined locally, so it stays true on an
	// air-gapped install where the release check has never once succeeded.
	AgentUnsupported AgentState = "unsupported"
)

// AgentSupported reports whether an agent build is at or above MinAgentVersion. An unparseable
// version is not called unsupported: we do not know what it is, and guessing wrong here paints a
// working fleet red.
func AgentSupported(current string) bool {
	if normalize(current) == "" {
		return true
	}
	return !IsNewer(current, MinAgentVersion)
}

// ClassifyAgent places a node's agent against the floor and the newest known release. latest may be
// empty — an install that cannot reach GitHub still gets the unsupported verdict, which is the one
// that matters and the one that needs no network.
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

// LatestVersion returns the newest release recorded for this component, or "" when the check has
// never succeeded. One indexed row: cheap enough to read per request rather than cached in memory,
// where it would go stale against the cron that writes it.
func (s *Service) LatestVersion() string {
	st, err := s.Status()
	if err != nil {
		return ""
	}
	return st.LatestVersion
}
