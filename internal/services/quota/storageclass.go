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

package quota

import (
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// An empty Allowed binds nothing; the node's own default decides.
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

// EffectiveStorageClasses binds nothing on a nil service or with enforcement off.
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

// RequireStorageClass makes a fast disk a plan feature rather than an admin convenience.
func (s *Service) RequireStorageClass(workspaceID uint, name string) error {
	if !s.Enabled() {
		return nil
	}
	if s.EffectiveStorageClasses(workspaceID).Allows(name) {
		return nil
	}
	return fmt.Errorf("%w: storage class %q", ErrCapabilityDenied, name)
}
