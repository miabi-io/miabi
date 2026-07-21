// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestResourceLimitFromPlan(t *testing.T) {
	l := limitsFromPlan(&models.Plan{MaxApps: 3, MaxVolumes: 0, MaxAPIKeys: models.Unlimited})
	if got := resourceLimit(l, ResourceApps); got != 3 {
		t.Errorf("apps limit = %d, want 3", got)
	}
	if got := resourceLimit(l, ResourceVolumes); got != 0 {
		t.Errorf("volumes limit = %d, want 0 (none)", got)
	}
	if got := resourceLimit(l, ResourceAPIKeys); got != models.Unlimited {
		t.Errorf("api_keys limit = %d, want unlimited(-1)", got)
	}
}

// TestRunnerQuotaWiring covers the runners plan dimension end to end: the
// numeric MaxRunners limit resolves and overrides like any counted resource, and
// the platform-runners capability resolves through the plan and its override.
func TestRunnerQuotaWiring(t *testing.T) {
	l := limitsFromPlan(&models.Plan{MaxRunners: 2, AllowPlatformRunners: true})
	if got := resourceLimit(l, ResourceRunners); got != 2 {
		t.Errorf("runners limit = %d, want 2", got)
	}
	if !l.AllowPlatformRunners {
		t.Error("plan should grant platform runners")
	}
	// Overrides win over the plan for both the limit and the capability.
	zero := 0
	no := false
	got := applyOverride(l, &models.WorkspaceQuota{MaxRunners: &zero, AllowPlatformRunners: &no})
	if got.MaxRunners != 0 {
		t.Errorf("override MaxRunners = %d, want 0 (none)", got.MaxRunners)
	}
	if got.AllowPlatformRunners {
		t.Error("override should revoke platform runners")
	}
	// The unlimited (platform) plan grants runners without limit.
	if u := unlimited(); u.MaxRunners != -1 || !u.AllowPlatformRunners {
		t.Errorf("unlimited runners = %d / shared=%v, want -1 / true", u.MaxRunners, u.AllowPlatformRunners)
	}
}

// TestGPUQuotaWiring covers the GPU capability + MaxGPUs limit resolving through
// the plan, its override, and the unlimited platform plan — the same triplet as
// every other metered resource.
func TestGPUQuotaWiring(t *testing.T) {
	l := limitsFromPlan(&models.Plan{MaxGPUs: 2, AllowGPU: true})
	if got := resourceLimit(l, ResourceGPUs); got != 2 {
		t.Errorf("gpus limit = %d, want 2", got)
	}
	if !l.AllowGPU {
		t.Error("plan should grant GPU capability")
	}
	// Overrides win over the plan for both the limit and the capability.
	zero := 0
	no := false
	got := applyOverride(l, &models.WorkspaceQuota{MaxGPUs: &zero, AllowGPU: &no})
	if got.MaxGPUs != 0 {
		t.Errorf("override MaxGPUs = %d, want 0 (none)", got.MaxGPUs)
	}
	if got.AllowGPU {
		t.Error("override should revoke GPU capability")
	}
	// The unlimited (platform) plan grants GPUs without limit.
	if u := unlimited(); u.MaxGPUs != -1 || !u.AllowGPU {
		t.Errorf("unlimited gpus = %d / allow=%v, want -1 / true", u.MaxGPUs, u.AllowGPU)
	}
	// A disabled service treats the GPU capability as granted (no gating) and
	// the GPU request check as a no-op.
	off := NewService(nil, nil, nil, nil, nil, false)
	if err := off.Require(1, CapGPU); err != nil {
		t.Errorf("disabled Require(CapGPU) = %v, want nil", err)
	}
	if err := off.CheckGPURequest(1, 100, 0); err != nil {
		t.Errorf("disabled CheckGPURequest = %v, want nil", err)
	}
}

func TestCustomBuilderCapabilityWiring(t *testing.T) {
	// Off by default in a plan; granted when the plan sets it.
	if l := limitsFromPlan(&models.Plan{}); l.AllowCustomBuilder {
		t.Error("plan default should deny a custom builder")
	}
	l := limitsFromPlan(&models.Plan{AllowCustomBuilder: true})
	if !l.AllowCustomBuilder {
		t.Error("plan should grant a custom builder")
	}
	// A workspace override wins over the plan.
	no := false
	if got := applyOverride(l, &models.WorkspaceQuota{AllowCustomBuilder: &no}); got.AllowCustomBuilder {
		t.Error("override should revoke the custom builder")
	}
	// The unlimited (platform) plan grants it.
	if !unlimited().AllowCustomBuilder {
		t.Error("unlimited should grant a custom builder")
	}
}

func TestOfficialImageUserCapabilityWiring(t *testing.T) {
	// Off by default in a plan; granted when the plan sets it.
	if l := limitsFromPlan(&models.Plan{}); l.AllowOfficialImageUser {
		t.Error("plan default should not exempt official-template apps")
	}
	l := limitsFromPlan(&models.Plan{AllowOfficialImageUser: true})
	if !l.AllowOfficialImageUser {
		t.Error("plan should grant the official-image-user exemption")
	}
	// A workspace override wins over the plan.
	no := false
	if got := applyOverride(l, &models.WorkspaceQuota{AllowOfficialImageUser: &no}); got.AllowOfficialImageUser {
		t.Error("override should revoke the exemption")
	}
	yes := true
	base := limitsFromPlan(&models.Plan{})
	if got := applyOverride(base, &models.WorkspaceQuota{AllowOfficialImageUser: &yes}); !got.AllowOfficialImageUser {
		t.Error("override should grant the exemption")
	}
	// The unlimited (platform) plan grants it.
	if !unlimited().AllowOfficialImageUser {
		t.Error("unlimited should grant the exemption")
	}
}

func TestNilPlanIsUnlimited(t *testing.T) {
	l := limitsFromPlan(nil)
	if resourceLimit(l, ResourceApps) != -1 || !l.AllowCustomTLS {
		t.Error("nil plan should resolve to unlimited + all capabilities")
	}
}

func TestApplyOverride(t *testing.T) {
	base := limitsFromPlan(&models.Plan{MaxApps: 3, AllowCustomTLS: false})
	five := 5
	yes := true
	got := applyOverride(base, &models.WorkspaceQuota{MaxApps: &five, AllowCustomTLS: &yes})
	if got.MaxApps != 5 {
		t.Errorf("override MaxApps = %d, want 5", got.MaxApps)
	}
	if !got.AllowCustomTLS {
		t.Error("override should enable custom TLS")
	}
	// An unset (nil) override field inherits the plan value.
	if applyOverride(base, &models.WorkspaceQuota{}).MaxApps != 3 {
		t.Error("nil override field should inherit the plan")
	}
}

func TestSecurityProfileResolution(t *testing.T) {
	// Plan profile resolves and normalizes ("" -> default).
	if got := limitsFromPlan(&models.Plan{}).SecurityProfile; got != models.SecurityProfileDefault {
		t.Errorf("empty plan profile = %q, want default", got)
	}
	if got := limitsFromPlan(&models.Plan{SecurityProfile: "restricted"}).SecurityProfile; got != models.SecurityProfileRestricted {
		t.Errorf("plan profile = %q, want restricted", got)
	}
	// A nil override field inherits; a set field overrides.
	base := limitsFromPlan(&models.Plan{SecurityProfile: "restricted"})
	if got := applyOverride(base, &models.WorkspaceQuota{}).SecurityProfile; got != models.SecurityProfileRestricted {
		t.Errorf("nil override should inherit restricted, got %q", got)
	}
	def := models.SecurityProfileDefault
	if got := applyOverride(base, &models.WorkspaceQuota{SecurityProfile: &def}).SecurityProfile; got != models.SecurityProfileDefault {
		t.Errorf("override should relax to default, got %q", got)
	}
	// The unlimited (platform) plan is never restricted.
	if unlimited().SecurityProfile != models.SecurityProfileDefault {
		t.Error("unlimited limits must not be restricted")
	}
}

func TestQuotaErrorMatchesSentinel(t *testing.T) {
	err := error(&QuotaError{Resource: ResourceApps, Used: 3, Limit: 3})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Error("QuotaError should match ErrQuotaExceeded")
	}
}

// Quota and capability errors carry a stable machine code for the API envelope
// while still matching their sentinels via errors.Is.
func TestErrorCodes(t *testing.T) {
	var coder interface{ Code() string }

	qe := error(&QuotaError{Resource: ResourceApps, Used: 3, Limit: 3})
	if !errors.As(qe, &coder) || coder.Code() != "QUOTA_EXCEEDED" {
		t.Errorf("QuotaError code = %q, want QUOTA_EXCEEDED", codeOf(qe))
	}

	ce := quotaExceeded("workspace storage %d MB exceeds the %d MB limit", 100, 50)
	if !errors.Is(ce, ErrQuotaExceeded) || codeOf(ce) != "QUOTA_EXCEEDED" {
		t.Errorf("aggregate quota error: is=%v code=%q", errors.Is(ce, ErrQuotaExceeded), codeOf(ce))
	}

	cap := (&Service{enforce: true}).capabilityErrForTest()
	if !errors.Is(cap, ErrCapabilityDenied) || codeOf(cap) != "CAPABILITY_DENIED" {
		t.Errorf("capability error: is=%v code=%q", errors.Is(cap, ErrCapabilityDenied), codeOf(cap))
	}
}

func codeOf(err error) string {
	var c interface{ Code() string }
	if errors.As(err, &c) {
		return c.Code()
	}
	return ""
}

func (s *Service) capabilityErrForTest() error {
	return &codedError{code: "CAPABILITY_DENIED", msg: "capability not allowed by plan: custom_tls", base: ErrCapabilityDenied}
}

// A disabled (or nil) service is a no-op: every check passes without touching
// the repository, so callers can always invoke it unconditionally.
func TestDisabledServiceIsNoop(t *testing.T) {
	var s *Service // nil receiver
	if s.Enabled() {
		t.Error("nil service should report disabled")
	}
	if err := s.CheckCreate(1, ResourceApps, 999); err != nil {
		t.Errorf("nil service CheckCreate should pass, got %v", err)
	}
	if err := s.Require(1, CapCustomTLS); err != nil {
		t.Errorf("nil service Require should pass, got %v", err)
	}

	off := NewService(nil, nil, nil, nil, nil, false)
	if off.Enabled() {
		t.Error("service with enforce=false should report disabled")
	}
	if err := off.CheckCreate(1, ResourceApps, 999); err != nil {
		t.Errorf("disabled CheckCreate should pass, got %v", err)
	}
}

// fakeEdition is a test EditionGate granting an explicit set of flags.
type fakeEdition map[string]bool

func (f fakeEdition) Has(flag string) bool { return f[flag] }

// The entitlement gate drives whether the Enterprise-only restricted security
// profile applies: with no gate wired (Community) the flag reads false, so the
// resolver later clamps the security profile back to default.
func TestEntitledGate(t *testing.T) {
	s := NewService(nil, nil, nil, nil, nil, true)
	if s.entitled(flagSecurityProfile) {
		t.Error("no gate wired should report nothing entitled (community)")
	}
	s.SetEdition(fakeEdition{flagSecurityProfile: true})
	if !s.entitled(flagSecurityProfile) {
		t.Error("security_profile should be entitled")
	}
	// SetEdition(nil) reverts to the permissive community default.
	s.SetEdition(nil)
	if s.entitled(flagSecurityProfile) {
		t.Error("nil gate should report nothing entitled")
	}
}
