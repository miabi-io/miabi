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

package database

import (
	"context"

	"github.com/miabi-io/miabi/internal/services/crypto"
)

// Reencrypt re-encrypts the workspace's database secrets (instance admin
// passwords + libSQL keys, and each logical database's password) under the
// workspace's active DEK. Idempotent; returns the number rewritten.
func (s *Service) Reencrypt(ctx context.Context, workspaceID uint) (int, error) {
	insts, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range insts {
		inst := &insts[i]
		instChanged := false
		for _, f := range []*string{&inst.AdminPasswordEnc, &inst.JWTPrivateKeyEnc} {
			v, changed, rerr := crypto.Reencrypt(workspaceID, *f)
			if rerr != nil {
				return n, rerr
			}
			if changed {
				*f = v
				instChanged = true
			}
		}
		if instChanged {
			if err := s.repo.Update(inst); err != nil {
				return n, err
			}
			n++
		}
		dbs, derr := s.repo.ListDatabases(inst.ID)
		if derr != nil {
			return n, derr
		}
		for j := range dbs {
			v, changed, rerr := crypto.Reencrypt(workspaceID, dbs[j].PasswordEnc)
			if rerr != nil {
				return n, rerr
			}
			ro, roChanged := dbs[j].ReadOnlyPasswordEnc, false
			if ro != "" {
				if ro, roChanged, rerr = crypto.Reencrypt(workspaceID, ro); rerr != nil {
					return n, rerr
				}
			}
			if changed || roChanged {
				dbs[j].PasswordEnc, dbs[j].ReadOnlyPasswordEnc = v, ro
				if err := s.repo.UpdateDatabase(&dbs[j]); err != nil {
					return n, err
				}
				n++
			}
		}
	}
	return n, nil
}
