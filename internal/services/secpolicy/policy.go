// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package secpolicy is the Security Center: platform rules written once and enforced at every
// point a tenant could get around them. A rule is scoped (platform, plan, organization,
// workspace), the most specific scope wins but may only tighten the platform rule, and every
// rule can run in audit mode first, recording what it would have blocked without blocking it.
//
// Without the security_policies entitlement, or with MIABI_SECURITY_POLICIES=off, every
// evaluation returns Community behaviour; stored rules are kept, not applied.
package secpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Kinds of policy.
const (
	KindPorts = "ports"
	// KindPolicy marks events about policies themselves (a change), not a decision.
	KindPolicy = "policy"
)

// Modes a policy runs in.
const (
	ModeOff     = "off"
	ModeAudit   = "audit"
	ModeEnforce = "enforce"
)

// Scopes, from widest to narrowest.
const (
	ScopePlatform     = "platform"
	ScopePlan         = "plan"
	ScopeOrganization = "organization"
	ScopeWorkspace    = "workspace"
)

// Decisions recorded as events.
const (
	DecisionDeny      = "deny"
	DecisionWouldDeny = "would_deny"
	DecisionChange    = "change"
)

// Host-port policy modes (PortsSpec.Mode).
const (
	PortsApproval            = "approval"
	PortsAutoApproveInRange  = "auto_approve_in_range"
	PortsRejectAll           = "reject_all"
	PortsExistingKeep        = "keep"
	PortsExistingReport      = "report"
	PortsExistingRevoke      = "revoke"
	portsSpecVersion         = 1
	maxPortNumber            = 65535
	defaultPortsExistingMode = PortsExistingKeep
)

var (
	// ErrInvalidSpec means a policy spec failed validation; the message says why.
	ErrInvalidSpec = errors.New("invalid policy spec")
	// ErrLoosens refuses a narrower rule that would reopen what the platform rule closed.
	ErrLoosens = errors.New("a narrower scope may only tighten the platform rule, which does not allow exceptions")
)

// PortRange is an inclusive host-port window.
type PortRange struct {
	From      int      `json:"from"`
	To        int      `json:"to"`
	Protocols []string `json:"protocols,omitempty"` // empty = tcp and udp
}

func (r PortRange) contains(port int, proto string) bool {
	if port < r.From || port > r.To {
		return false
	}
	return len(r.Protocols) == 0 || slices.Contains(r.Protocols, proto)
}

// PortsSpec is the host-port policy.
type PortsSpec struct {
	V    int    `json:"v"`
	Mode string `json:"mode"` // approval | auto_approve_in_range | reject_all
	// AllowedRanges are auto-approved under auto_approve_in_range.
	AllowedRanges []PortRange `json:"allowed_ranges,omitempty"`
	// BindAddress publishes approved ports on this address instead of every interface.
	BindAddress string `json:"bind_address,omitempty"`
	// PrivilegedBypass keeps today's auto-approval for privileged workspaces. nil means true.
	PrivilegedBypass *bool `json:"privileged_bypass,omitempty"`
	// Existing decides what happens to bindings approved before a reject_all rule.
	Existing string `json:"existing,omitempty"` // keep | report | revoke
}

// privilegedBypass reports the effective bypass, defaulting to today's behaviour.
func (s PortsSpec) privilegedBypass() bool { return s.PrivilegedBypass == nil || *s.PrivilegedBypass }

// ParsePortsSpec decodes and validates a ports spec, filling defaults.
func ParsePortsSpec(raw string) (PortsSpec, error) {
	var s PortsSpec
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, fmt.Errorf("%w: %v", ErrInvalidSpec, err)
	}
	if s.V == 0 {
		s.V = portsSpecVersion
	}
	if s.V != portsSpecVersion {
		return s, fmt.Errorf("%w: unsupported ports spec version %d", ErrInvalidSpec, s.V)
	}
	if s.Mode == "" {
		s.Mode = PortsApproval
	}
	if !slices.Contains([]string{PortsApproval, PortsAutoApproveInRange, PortsRejectAll}, s.Mode) {
		return s, fmt.Errorf("%w: unknown ports mode %q", ErrInvalidSpec, s.Mode)
	}
	if s.Existing == "" {
		s.Existing = defaultPortsExistingMode
	}
	if !slices.Contains([]string{PortsExistingKeep, PortsExistingReport, PortsExistingRevoke}, s.Existing) {
		return s, fmt.Errorf("%w: unknown existing mode %q", ErrInvalidSpec, s.Existing)
	}
	for i, r := range s.AllowedRanges {
		if r.From < 1 || r.To > maxPortNumber || r.From > r.To {
			return s, fmt.Errorf("%w: range %d is not a valid port window", ErrInvalidSpec, i+1)
		}
		for _, p := range r.Protocols {
			if p != "tcp" && p != "udp" {
				return s, fmt.Errorf("%w: range %d has unknown protocol %q", ErrInvalidSpec, i+1, p)
			}
		}
	}
	if s.Mode == PortsAutoApproveInRange && len(s.AllowedRanges) == 0 {
		return s, fmt.Errorf("%w: auto_approve_in_range needs at least one allowed range", ErrInvalidSpec)
	}
	if s.BindAddress != "" {
		if _, err := netip.ParseAddr(s.BindAddress); err != nil {
			return s, fmt.Errorf("%w: bind_address %q is not an IP address", ErrInvalidSpec, s.BindAddress)
		}
	}
	return s, nil
}

// Encode renders a spec for storage.
func (s PortsSpec) Encode() string {
	b, _ := json.Marshal(s)
	return string(b)
}

func modeRank(m string) int {
	switch m {
	case ModeEnforce:
		return 2
	case ModeAudit:
		return 1
	}
	return 0
}

func portsModeRank(m string) int {
	switch m {
	case PortsRejectAll:
		return 2
	case PortsApproval:
		return 1
	}
	return 0
}

// portsLoosens reports whether narrow would reopen anything base closes.
func portsLoosens(baseMode string, base PortsSpec, narrowMode string, narrow PortsSpec) bool {
	if modeRank(narrowMode) < modeRank(baseMode) {
		return true
	}
	if portsModeRank(narrow.Mode) < portsModeRank(base.Mode) {
		return true
	}
	if !base.privilegedBypass() && narrow.privilegedBypass() {
		return true
	}
	if base.BindAddress != "" && narrow.BindAddress != base.BindAddress {
		return true
	}
	if base.Mode == PortsAutoApproveInRange && narrow.Mode == PortsAutoApproveInRange {
		for _, r := range narrow.AllowedRanges {
			if !rangeCovered(r, base.AllowedRanges) {
				return true
			}
		}
	}
	return false
}

// rangeCovered reports whether every port and protocol of r is auto-approved by base too.
func rangeCovered(r PortRange, base []PortRange) bool {
	protos := r.Protocols
	if len(protos) == 0 {
		protos = []string{"tcp", "udp"}
	}
	for _, p := range protos {
		for port := r.From; port <= r.To; port++ {
			ok := false
			for _, b := range base {
				if b.contains(port, p) {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
		}
	}
	return true
}

// ValidScope reports whether a scope type is known and its id is consistent with it.
func ValidScope(scopeType string, scopeID uint) error {
	switch scopeType {
	case ScopePlatform:
		if scopeID != 0 {
			return fmt.Errorf("%w: the platform scope takes no id", ErrInvalidSpec)
		}
	case ScopePlan, ScopeOrganization, ScopeWorkspace:
		if scopeID == 0 {
			return fmt.Errorf("%w: a %s scope needs an id", ErrInvalidSpec, scopeType)
		}
	default:
		return fmt.Errorf("%w: unknown scope %q", ErrInvalidSpec, scopeType)
	}
	return nil
}

// ValidMode reports whether m is a policy mode.
func ValidMode(m string) bool { return m == ModeOff || m == ModeAudit || m == ModeEnforce }

func scopeLabel(scopeType string, scopeID uint) string {
	if scopeType == ScopePlatform {
		return ScopePlatform
	}
	return fmt.Sprintf("%s:%d", scopeType, scopeID)
}
