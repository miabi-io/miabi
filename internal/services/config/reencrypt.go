// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"context"

	"github.com/miabi-io/miabi/internal/services/crypto"
)

// Reencrypt re-encrypts this workspace's config content under its active DEK.
// Registered with the keyring like every other ciphertext owner; skipping it
// would orphan config files on rotation.
func (s *Service) Reencrypt(ctx context.Context, workspaceID uint) (int, error) {
	rows, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range rows {
		v, changed, rerr := crypto.Reencrypt(workspaceID, rows[i].DataEnc)
		if rerr != nil {
			return n, rerr
		}
		if changed {
			rows[i].DataEnc = v
			if uerr := s.repo.Update(&rows[i]); uerr != nil {
				return n, uerr
			}
			n++
		}
	}
	return n, nil
}
