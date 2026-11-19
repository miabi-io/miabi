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

package job

import (
	"errors"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/appref"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

var ErrInvalidEnv = errors.New("invalid job environment")

// SealEnv validates incoming variables and encrypts secret values; a masked secret keeps its stored value from current.
func (s *Service) SealEnv(workspaceID uint, in, current models.JobEnv) (models.JobEnv, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(in))
	out := make(models.JobEnv, 0, len(in))
	for _, v := range in {
		key := strings.TrimSpace(v.Key)
		if key == "" || strings.ContainsAny(key, "= \t\r\n\x00") {
			return nil, fmt.Errorf("%w: %q is not a valid variable name", ErrInvalidEnv, v.Key)
		}
		if seen[key] {
			return nil, fmt.Errorf("%w: %s is set twice", ErrInvalidEnv, key)
		}
		seen[key] = true
		if err := appref.ValidateFields(v.Value); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalidEnv, key, err)
		}
		if !v.IsSecret {
			out = append(out, models.JobEnvVar{Key: key, Value: v.Value})
			continue
		}
		if v.Value == models.JobEnvMask {
			prev, ok := current.Lookup(key)
			if !ok || !prev.IsSecret {
				return nil, fmt.Errorf("%w: %s has no stored secret value to keep", ErrInvalidEnv, key)
			}
			out = append(out, prev)
			continue
		}
		enc, err := crypto.EncryptWS(workspaceID, v.Value)
		if err != nil {
			return nil, fmt.Errorf("encrypt %s: %w", key, err)
		}
		out = append(out, models.JobEnvVar{Key: key, Value: enc, IsSecret: true})
	}
	return out, nil
}

// SameEnv compares a sealed env with incoming plaintext; a secret counts as unchanged only when sent back masked.
func SameEnv(sealed, in models.JobEnv) bool {
	if len(sealed) != len(in) {
		return false
	}
	for _, v := range in {
		prev, ok := sealed.Lookup(strings.TrimSpace(v.Key))
		if !ok || prev.IsSecret != v.IsSecret {
			return false
		}
		if v.IsSecret {
			if v.Value != models.JobEnvMask {
				return false
			}
			continue
		}
		if prev.Value != v.Value {
			return false
		}
	}
	return true
}

func inheritOrDefault(v *bool) *bool {
	b := models.InheritsEnv(v)
	return &b
}
