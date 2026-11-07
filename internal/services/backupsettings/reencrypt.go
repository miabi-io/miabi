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

package backupsettings

import (
	"context"
	"errors"

	"github.com/miabi-io/miabi/internal/services/crypto"
	"gorm.io/gorm"
)

// Reencrypt is idempotent.
func (s *Service) Reencrypt(ctx context.Context, workspaceID uint) (int, error) {
	st, err := s.repo.FindByWorkspace(workspaceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	changed := false
	for _, f := range []*string{&st.S3SecretKeyEnc, &st.BundlePassphraseEnc, &st.BackupPassphraseEnc} {
		v, ch, rerr := crypto.Reencrypt(workspaceID, *f)
		if rerr != nil {
			return 0, rerr
		}
		if ch {
			*f = v
			changed = true
		}
	}
	if !changed {
		return 0, nil
	}
	if err := s.repo.Upsert(st); err != nil {
		return 0, err
	}
	return 1, nil
}
