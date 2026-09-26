// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package secpolicy

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func portsRow(scope string, id uint, mode string, spec PortsSpec, allowExceptions bool) models.SecurityPolicy {
	return models.SecurityPolicy{Kind: KindPorts, ScopeType: scope, ScopeID: id, Mode: mode, Spec: spec.Encode(), AllowExceptions: allowExceptions}
}

func TestParsePortsSpecDefaultsAndRefusals(t *testing.T) {
	s, err := ParsePortsSpec("")
	if err != nil || s.Mode != PortsApproval || s.Existing != PortsExistingKeep || !s.privilegedBypass() {
		t.Fatalf("defaults = %+v, %v", s, err)
	}
	for name, raw := range map[string]string{
		"unknown mode":     `{"mode":"sometimes"}`,
		"future version":   `{"v":2}`,
		"inverted range":   `{"mode":"auto_approve_in_range","allowed_ranges":[{"from":200,"to":100}]}`,
		"range needed":     `{"mode":"auto_approve_in_range"}`,
		"bad protocol":     `{"mode":"auto_approve_in_range","allowed_ranges":[{"from":1,"to":2,"protocols":["sctp"]}]}`,
		"bad bind address": `{"bind_address":"localhost"}`,
		"unknown existing": `{"existing":"forget"}`,
		"not json":         `{`,
	} {
		if _, err := ParsePortsSpec(raw); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: err = %v, want ErrInvalidSpec", name, err)
		}
	}
}

// A narrower scope may only tighten the platform rule unless the platform allows exceptions.
func TestResolveNarrowerMayOnlyTighten(t *testing.T) {
	platform := portsRow(ScopePlatform, 0, ModeEnforce, PortsSpec{Mode: PortsApproval}, false)
	stricter := portsRow(ScopeWorkspace, 7, ModeEnforce, PortsSpec{Mode: PortsRejectAll}, false)
	looser := portsRow(ScopeWorkspace, 8, ModeEnforce, PortsSpec{Mode: PortsAutoApproveInRange, AllowedRanges: []PortRange{{From: 1, To: 10}}}, false)
	offOverride := portsRow(ScopeOrganization, 3, ModeOff, PortsSpec{Mode: PortsRejectAll}, false)
	rows := []models.SecurityPolicy{platform, stricter, looser, offOverride}

	if got := resolve(rows, 7, 0, 0); got.ScopeType != ScopeWorkspace {
		t.Errorf("stricter workspace rule ignored: got %s", got.ScopeType)
	}
	if got := resolve(rows, 8, 0, 0); got.ScopeType != ScopePlatform {
		t.Errorf("looser workspace rule applied without an exception: got %s", got.ScopeType)
	}
	if got := resolve(rows, 99, 0, 3); got.ScopeType != ScopePlatform {
		t.Errorf("an organization rule switching enforcement off was applied: got %s", got.ScopeType)
	}
	rows[0].AllowExceptions = true
	if got := resolve(rows, 8, 0, 0); got.ScopeType != ScopeWorkspace {
		t.Errorf("exception allowed but the looser rule was not applied: got %s", got.ScopeType)
	}
}

func TestResolveSpecificityOrder(t *testing.T) {
	rows := []models.SecurityPolicy{
		portsRow(ScopePlan, 2, ModeEnforce, PortsSpec{Mode: PortsApproval}, false),
		portsRow(ScopeOrganization, 3, ModeEnforce, PortsSpec{Mode: PortsApproval}, false),
		portsRow(ScopeWorkspace, 4, ModeEnforce, PortsSpec{Mode: PortsApproval}, false),
	}
	if got := resolve(rows, 4, 2, 3); got.ScopeType != ScopeWorkspace {
		t.Errorf("got %s, want workspace", got.ScopeType)
	}
	if got := resolve(rows, 5, 2, 3); got.ScopeType != ScopeOrganization {
		t.Errorf("got %s, want organization", got.ScopeType)
	}
	if got := resolve(rows, 5, 2, 0); got.ScopeType != ScopePlan {
		t.Errorf("got %s, want plan", got.ScopeType)
	}
	if got := resolve(rows, 5, 9, 9); got != nil {
		t.Errorf("got %+v, want no rule", got)
	}
}

func TestPortsLoosens(t *testing.T) {
	no := false
	base := PortsSpec{Mode: PortsAutoApproveInRange, AllowedRanges: []PortRange{{From: 30000, To: 30100, Protocols: []string{"tcp"}}}, PrivilegedBypass: &no, BindAddress: "10.0.0.1"}
	within := base
	within.AllowedRanges = []PortRange{{From: 30010, To: 30020, Protocols: []string{"tcp"}}}
	if portsLoosens(ModeEnforce, base, ModeEnforce, within) {
		t.Error("a sub-range was treated as loosening")
	}
	wider := base
	wider.AllowedRanges = []PortRange{{From: 29000, To: 30020, Protocols: []string{"tcp"}}}
	if !portsLoosens(ModeEnforce, base, ModeEnforce, wider) {
		t.Error("a wider auto-approve range was not treated as loosening")
	}
	udp := base
	udp.AllowedRanges = []PortRange{{From: 30010, To: 30020}}
	if !portsLoosens(ModeEnforce, base, ModeEnforce, udp) {
		t.Error("adding udp to the range was not treated as loosening")
	}
	bypass := within
	bypass.PrivilegedBypass = nil
	if !portsLoosens(ModeEnforce, base, ModeEnforce, bypass) {
		t.Error("re-enabling the privileged bypass was not treated as loosening")
	}
	anyAddr := within
	anyAddr.BindAddress = ""
	if !portsLoosens(ModeEnforce, base, ModeEnforce, anyAddr) {
		t.Error("dropping the bind address was not treated as loosening")
	}
	if !portsLoosens(ModeEnforce, base, ModeAudit, within) {
		t.Error("downgrading enforce to audit was not treated as loosening")
	}
}

func TestDecidePortRequest(t *testing.T) {
	no := false
	cases := []struct {
		name        string
		mode        string
		spec        PortsSpec
		req         PortRequest
		allowed     bool
		autoApprove bool
		denied      bool
	}{
		{"approval, ordinary", ModeEnforce, PortsSpec{Mode: PortsApproval}, PortRequest{HostPort: 30001}, true, false, false},
		{"approval, privileged bypass", ModeEnforce, PortsSpec{Mode: PortsApproval}, PortRequest{HostPort: 25, Privileged: true}, true, true, false},
		{"approval, bypass off", ModeEnforce, PortsSpec{Mode: PortsApproval, PrivilegedBypass: &no}, PortRequest{HostPort: 25, Privileged: true}, true, false, false},
		{"in range", ModeEnforce, PortsSpec{Mode: PortsAutoApproveInRange, AllowedRanges: []PortRange{{From: 30000, To: 30100}}}, PortRequest{HostPort: 30050}, true, true, false},
		{"out of range goes to review", ModeEnforce, PortsSpec{Mode: PortsAutoApproveInRange, AllowedRanges: []PortRange{{From: 30000, To: 30100}}}, PortRequest{HostPort: 31000}, true, false, false},
		{"range is per protocol", ModeEnforce, PortsSpec{Mode: PortsAutoApproveInRange, AllowedRanges: []PortRange{{From: 30000, To: 30100, Protocols: []string{"tcp"}}}}, PortRequest{HostPort: 30050, Protocol: "udp"}, true, false, false},
		{"reject all", ModeEnforce, PortsSpec{Mode: PortsRejectAll}, PortRequest{HostPort: 30001, Privileged: true}, false, false, true},
		{"reject all in audit keeps today's behaviour", ModeAudit, PortsSpec{Mode: PortsRejectAll}, PortRequest{HostPort: 30001, Privileged: true}, true, true, true},
		{"audit never auto-approves by range", ModeAudit, PortsSpec{Mode: PortsAutoApproveInRange, AllowedRanges: []PortRange{{From: 30000, To: 30100}}}, PortRequest{HostPort: 30050}, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, denied := decidePortRequest(tc.mode, tc.spec, tc.req)
			if d.Allowed != tc.allowed || d.AutoApprove != tc.autoApprove || (denied != "") != tc.denied {
				t.Fatalf("got allowed=%v auto=%v denied=%q", d.Allowed, d.AutoApprove, denied)
			}
		})
	}
}

// A binding approved before reject_all keeps publishing under keep/report, and stops under revoke
// or when it was written after the rule.
func TestDecidePublish(t *testing.T) {
	policyAt := time.Now()
	before := models.PortBinding{HostPort: 30001, Protocol: "tcp", UpdatedAt: policyAt.Add(-time.Hour)}
	after := models.PortBinding{HostPort: 30001, Protocol: "tcp", UpdatedAt: policyAt.Add(time.Hour)}

	if d, _ := decidePublish(ModeEnforce, PortsSpec{Mode: PortsRejectAll, Existing: PortsExistingKeep}, policyAt, before); !d.Allowed {
		t.Error("a grandfathered binding was not published")
	}
	if d, _ := decidePublish(ModeEnforce, PortsSpec{Mode: PortsRejectAll, Existing: PortsExistingKeep}, policyAt, after); d.Allowed {
		t.Error("a binding written after reject_all was published")
	}
	if d, _ := decidePublish(ModeEnforce, PortsSpec{Mode: PortsRejectAll, Existing: PortsExistingRevoke}, policyAt, before); d.Allowed {
		t.Error("revoke still published an old binding")
	}
	if d, denied := decidePublish(ModeAudit, PortsSpec{Mode: PortsRejectAll}, policyAt, after); !d.Allowed || denied == "" {
		t.Error("audit mode must publish and report")
	}
	if d, _ := decidePublish(ModeEnforce, PortsSpec{Mode: PortsApproval, BindAddress: "127.0.0.1"}, policyAt, after); d.BindAddress != "127.0.0.1" {
		t.Errorf("bind address = %q", d.BindAddress)
	}
	if d, _ := decidePublish(ModeAudit, PortsSpec{Mode: PortsApproval, BindAddress: "127.0.0.1"}, policyAt, after); d.BindAddress != "" {
		t.Error("audit mode changed the bind address")
	}
}

func TestValidScope(t *testing.T) {
	if ValidScope(ScopePlatform, 0) != nil || ValidScope(ScopeWorkspace, 3) != nil {
		t.Error("valid scopes refused")
	}
	if ValidScope(ScopePlatform, 1) == nil || ValidScope(ScopeWorkspace, 0) == nil || ValidScope("galaxy", 1) == nil {
		t.Error("invalid scopes accepted")
	}
}

func TestParseAdminAccessSpec(t *testing.T) {
	_, a, err := ParseAdminAccessSpec(`{"required":true,"ttl":"1h","allowed_ips":["10.0.0.0/8","192.0.2.7"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Required || a.TTL != time.Hour || a.IdleTimeout != 10*time.Minute || len(a.AllowedIPs) != 2 {
		t.Fatalf("parsed = %+v", a)
	}
	for name, raw := range map[string]string{
		"unbuilt factor":    `{"factors":["pin"]}`,
		"idle beyond ttl":   `{"ttl":"5m","idle_timeout":"10m"}`,
		"too short":         `{"ttl":"10s"}`,
		"bad ip":            `{"allowed_ips":["office"]}`,
		"negative attempts": `{"max_attempts":-1}`,
	} {
		if _, _, err := ParseAdminAccessSpec(raw); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
