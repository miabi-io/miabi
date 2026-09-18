// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import (
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// StorageClassBinding is a workspace's resolved storage-class policy: which classes its plan may
// create volumes on, and which one an unstated create gets. An empty Allowed binds nothing — the
// node's own default decides, which is how every install without plan enforcement behaves.
type StorageClassBinding struct {
	Allowed []string
	Default string
}

// Allows reports whether the named class may be used. An empty Allowed list permits everything.
func (b StorageClassBinding) Allows(name string) bool {
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

// EffectiveStorageClasses resolves a workspace's storage-class binding (override -> plan). Safe on
// a nil service and when plan enforcement is off, both of which bind nothing.
func (s *Service) EffectiveStorageClasses(workspaceID uint) StorageClassBinding {
	if !s.Enabled() {
		return StorageClassBinding{}
	}
	var o *models.WorkspaceQuota
	if s.overrides != nil {
		o, _ = s.overrides.FindByWorkspace(workspaceID)
	}
	p := s.effectivePlan(workspaceID)
	b := StorageClassBinding{}
	if p != nil {
		b.Allowed, b.Default = p.StorageClasses, strings.TrimSpace(p.DefaultStorageClass)
	}
	if o != nil {
		if o.StorageClasses != nil {
			b.Allowed = *o.StorageClasses
		}
		if o.DefaultStorageClass != nil {
			b.Default = strings.TrimSpace(*o.DefaultStorageClass)
		}
	}
	return b
}

// RequireStorageClass returns ErrCapabilityDenied when the workspace's plan does not offer the
// class. It is the gate that makes a fast disk a plan feature rather than an admin convenience.
func (s *Service) RequireStorageClass(workspaceID uint, name string) error {
	if !s.Enabled() {
		return nil
	}
	if s.EffectiveStorageClasses(workspaceID).Allows(name) {
		return nil
	}
	return fmt.Errorf("%w: storage class %q", ErrCapabilityDenied, name)
}
