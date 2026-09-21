// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import (
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
)

// flagPlatformRunners is a local copy of enterprise.FlagPlatformRunners.
const flagPlatformRunners = "platform_runners"

// RunnerBinding is a workspace's resolved platform-runner policy: which of the shared runners its
// plan may build on. An empty Allowed binds nothing — the whole pool is offered, which is how every
// install that has not narrowed a plan behaves.
//
// It never applies to a workspace's own runners. The plan decides what the platform lends out; the
// machines a tenant registered are theirs.
type RunnerBinding struct {
	Allowed []string
}

// Allows reports whether the named shared runner may be used. An empty Allowed list permits every
// runner in the pool.
func (b RunnerBinding) Allows(name string) bool {
	if len(b.Allowed) == 0 {
		return true
	}
	for _, a := range b.Allowed {
		if a == name {
			return true
		}
	}
	return false
}

// EffectivePlatformRunners resolves which shared runners a workspace may use (override -> plan).
// The bool is false when no binding applies, because plan enforcement is off or platform_runners is
// not licensed — a lapsed license leaves the stored list inert rather than stranding the workspace.
func (s *Service) EffectivePlatformRunners(workspaceID uint) (RunnerBinding, bool) {
	if !s.Enabled() || !s.entitled(flagPlatformRunners) {
		return RunnerBinding{}, false
	}
	var o *models.WorkspaceQuota
	if s.overrides != nil {
		o, _ = s.overrides.FindByWorkspace(workspaceID)
	}
	return RunnerBinding{Allowed: platformRunnersFrom(s.effectivePlan(workspaceID), o)}, true
}

func platformRunnersFrom(p *models.Plan, o *models.WorkspaceQuota) []string {
	if o != nil && o.PlatformRunners != nil {
		return *o.PlatformRunners
	}
	if p == nil {
		return nil
	}
	return p.PlatformRunners
}

// PlatformRunnersLicensed reports whether the binding may be used at all, whether a plan sets one or
// not. The admin UI asks so it can explain a locked field.
func (s *Service) PlatformRunnersLicensed() bool { return s != nil && s.entitled(flagPlatformRunners) }

// RequirePlatformRunner returns ErrCapabilityDenied when the workspace's plan does not offer the
// named shared runner.
func (s *Service) RequirePlatformRunner(workspaceID uint, name string) error {
	binding, bound := s.EffectivePlatformRunners(workspaceID)
	if !bound || binding.Allows(name) {
		return nil
	}
	return &codedError{
		code: "CAPABILITY_DENIED",
		msg:  fmt.Sprintf("capability not allowed by plan: platform runner %q", name),
		base: ErrCapabilityDenied,
	}
}
