// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package secpolicy

import (
	"errors"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// ErrPortsDenied is returned when the host-port policy refuses a request.
var ErrPortsDenied = errors.New("host ports are disabled by platform policy")

// PortRequest describes a tenant asking for a host port.
type PortRequest struct {
	WorkspaceID uint
	UserID      uint
	HostPort    int
	Protocol    string
	Privileged  bool
	// Action is "request" (API) or "import" (stack/compose import).
	Action   string
	Resource string
}

// PortDecision is what the policy lets a request do.
type PortDecision struct {
	// Allowed is false only when an enforced rule refuses the request.
	Allowed bool
	// AutoApprove approves the request without review (an allowed range, or a privileged
	// workspace the policy still lets bypass review).
	AutoApprove bool
	// AutoApproveNote explains an auto-approval on the binding.
	AutoApproveNote string
	Reason          string
}

// Community behaviour: everything goes to review, privileged workspaces skip it.
func defaultPortDecision(privileged bool) PortDecision {
	d := PortDecision{Allowed: true, AutoApprove: privileged}
	if privileged {
		d.AutoApproveNote = "Auto-approved (privileged workspace)"
	}
	return d
}

// CheckPortRequest decides a host-port request and records a denial or would-be denial.
func (s *Service) CheckPortRequest(req PortRequest) PortDecision {
	if !s.Active() {
		return defaultPortDecision(req.Privileged)
	}
	pol, err := s.effective(KindPorts, req.WorkspaceID)
	if err != nil {
		logger.Error("security policy: resolve ports policy; applying Community behaviour", "error", err)
		return defaultPortDecision(req.Privileged)
	}
	if pol == nil || pol.Mode == ModeOff {
		return defaultPortDecision(req.Privileged)
	}
	spec, err := ParsePortsSpec(pol.Spec)
	if err != nil {
		logger.Error("security policy: stored ports spec is invalid; applying Community behaviour", "policy", pol.ID, "error", err)
		return defaultPortDecision(req.Privileged)
	}
	d, denied := decidePortRequest(pol.Mode, spec, req)
	if denied != "" {
		decision := DecisionDeny
		if pol.Mode == ModeAudit {
			decision = DecisionWouldDeny
		}
		s.record(&models.SecurityEvent{
			Kind: KindPorts, Action: req.Action, Decision: decision,
			UserID: optUint(req.UserID), WorkspaceID: optUint(req.WorkspaceID),
			Resource: req.Resource, PolicyID: &pol.ID, Scope: scopeLabel(pol.ScopeType, pol.ScopeID), Reason: denied,
		})
	}
	return d
}

// decidePortRequest is the judgement, without storage. denied names what an enforced rule refuses
// (or an audit rule would have); empty when nothing is refused. Audit mode keeps Community
// behaviour and only reports.
func decidePortRequest(mode string, spec PortsSpec, req PortRequest) (PortDecision, string) {
	proto := req.Protocol
	if proto != "udp" {
		proto = "tcp"
	}
	if spec.Mode == PortsRejectAll {
		reason := ErrPortsDenied.Error()
		if mode == ModeAudit {
			return defaultPortDecision(req.Privileged), reason
		}
		return PortDecision{Allowed: false, Reason: reason}, reason
	}
	if mode == ModeAudit {
		return defaultPortDecision(req.Privileged), ""
	}
	d := PortDecision{Allowed: true}
	if spec.Mode == PortsAutoApproveInRange && req.HostPort != 0 {
		for _, r := range spec.AllowedRanges {
			if r.contains(req.HostPort, proto) {
				d.AutoApprove = true
				d.AutoApproveNote = fmt.Sprintf("Auto-approved (within %d-%d by platform policy)", r.From, r.To)
				return d, ""
			}
		}
	}
	if req.Privileged && spec.privilegedBypass() {
		d.AutoApprove = true
		d.AutoApproveNote = "Auto-approved (privileged workspace)"
	}
	return d, ""
}

// PortPublish describes an approved binding about to be published by a deploy.
type PortPublish struct {
	WorkspaceID uint
	Binding     models.PortBinding
	Resource    string
}

// PublishDecision is whether and where an approved binding publishes.
type PublishDecision struct {
	Allowed bool
	// BindAddress, when set, is the host address the port publishes on instead of every interface.
	BindAddress string
	Reason      string
}

// CheckPortPublish re-checks an approved binding at deploy time, so a row approved before a
// reject_all rule (unless grandfathered by existing: keep/report), or written by any other path,
// cannot publish.
func (s *Service) CheckPortPublish(p PortPublish) PublishDecision {
	if !s.Active() {
		return PublishDecision{Allowed: true}
	}
	pol, err := s.effective(KindPorts, p.WorkspaceID)
	if err != nil || pol == nil || pol.Mode == ModeOff {
		return PublishDecision{Allowed: true}
	}
	spec, err := ParsePortsSpec(pol.Spec)
	if err != nil {
		return PublishDecision{Allowed: true}
	}
	d, denied := decidePublish(pol.Mode, spec, pol.UpdatedAt, p.Binding)
	if denied != "" {
		decision := DecisionDeny
		if pol.Mode == ModeAudit {
			decision = DecisionWouldDeny
		}
		s.record(&models.SecurityEvent{
			Kind: KindPorts, Action: "publish", Decision: decision, WorkspaceID: optUint(p.WorkspaceID),
			Resource: p.Resource, PolicyID: &pol.ID, Scope: scopeLabel(pol.ScopeType, pol.ScopeID), Reason: denied,
		})
	}
	return d
}

func decidePublish(mode string, spec PortsSpec, policyAt time.Time, b models.PortBinding) (PublishDecision, string) {
	if b.AdminAdopted {
		if mode == ModeAudit {
			return PublishDecision{Allowed: true}, ""
		}
		return PublishDecision{Allowed: true, BindAddress: spec.BindAddress}, ""
	}
	if mode == ModeAudit {
		if spec.Mode == PortsRejectAll && !grandfathered(spec, policyAt, b) {
			return PublishDecision{Allowed: true}, "would not publish: host ports are disabled by platform policy"
		}
		return PublishDecision{Allowed: true}, ""
	}
	if spec.Mode == PortsRejectAll && !grandfathered(spec, policyAt, b) {
		reason := fmt.Sprintf("host port %d/%s not published: host ports are disabled by platform policy", b.HostPort, b.Protocol)
		return PublishDecision{Allowed: false, Reason: reason}, reason
	}
	return PublishDecision{Allowed: true, BindAddress: spec.BindAddress}, ""
}

// grandfathered reports whether a binding approved before the rule keeps publishing.
func grandfathered(spec PortsSpec, policyAt time.Time, b models.PortBinding) bool {
	return spec.Existing != PortsExistingRevoke && !b.UpdatedAt.After(policyAt)
}

// PortsPolicyView is what a workspace member may know about the host-port policy, so the UI can
// hide "Request host port" and say why.
type PortsPolicyView struct {
	RequestsAllowed bool        `json:"requests_allowed"`
	Mode            string      `json:"mode"`
	AutoApprove     []PortRange `json:"auto_approve_ranges,omitempty"`
	Reason          string      `json:"reason,omitempty"`
}

// PortsView resolves the policy a workspace sees.
func (s *Service) PortsView(workspaceID uint) PortsPolicyView {
	v := PortsPolicyView{RequestsAllowed: true, Mode: PortsApproval}
	if !s.Active() {
		return v
	}
	pol, err := s.effective(KindPorts, workspaceID)
	if err != nil || pol == nil || pol.Mode != ModeEnforce {
		return v
	}
	spec, err := ParsePortsSpec(pol.Spec)
	if err != nil {
		return v
	}
	v.Mode = spec.Mode
	switch spec.Mode {
	case PortsRejectAll:
		v.RequestsAllowed = false
		v.Reason = ErrPortsDenied.Error()
	case PortsAutoApproveInRange:
		v.AutoApprove = spec.AllowedRanges
	}
	return v
}

// PlatformPorts returns the platform-level ports rule and its parsed spec, or nil when none is set.
func (s *Service) PlatformPorts() (*models.SecurityPolicy, *PortsSpec) {
	p, err := s.repo.FindScope(KindPorts, ScopePlatform, 0)
	if err != nil {
		return nil, nil
	}
	spec, err := ParsePortsSpec(p.Spec)
	if err != nil {
		return p, nil
	}
	return p, &spec
}

func optUint(v uint) *uint {
	if v == 0 {
		return nil
	}
	return &v
}
