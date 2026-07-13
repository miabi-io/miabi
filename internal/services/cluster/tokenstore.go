// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// SettingsTokenStore keeps the cluster agent token's HASH in the settings table.
//
// Only the hash. The plaintext token lives in the swarm service spec, which Docker
// already holds and distributes to the nodes — a second copy at rest would be a
// second thing to leak, and would buy nothing: we never need to read the token back,
// only to verify one an agent presents.
type SettingsTokenStore struct {
	repo *repositories.SettingRepository
}

func NewSettingsTokenStore(repo *repositories.SettingRepository) *SettingsTokenStore {
	return &SettingsTokenStore{repo: repo}
}

func (s *SettingsTokenStore) Get(key string) (string, error) {
	row, err := s.repo.Get(key)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", nil // never deployed: not an error, just no token
		}
		return "", err
	}
	return row.Value, nil
}

func (s *SettingsTokenStore) Set(key, value string) error {
	return s.repo.BulkUpsert([]models.Setting{{Key: key, Value: value}})
}
