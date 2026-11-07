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

package registry

import (
	"context"

	"github.com/miabi-io/miabi/internal/services/crypto"
)

// Reencrypt re-encrypts workspace secrets under the active DEK; idempotent.
func (s *Service) Reencrypt(ctx context.Context, workspaceID uint) (int, error) {
	rows, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range rows {
		v, changed, rerr := crypto.Reencrypt(workspaceID, rows[i].Secret)
		if rerr != nil {
			return n, rerr
		}
		if changed {
			rows[i].Secret = v
			if uerr := s.repo.Update(&rows[i]); uerr != nil {
				return n, uerr
			}
			n++
		}
	}
	return n, nil
}
