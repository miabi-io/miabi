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

package repositories

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type APIKeyRepository struct {
	db *gorm.DB
}

func NewAPIKeyRepository(db *gorm.DB) *APIKeyRepository { return &APIKeyRepository{db: db} }

func (r *APIKeyRepository) Create(k *models.APIKey) error {
	return r.db.Create(k).Error
}

func (r *APIKeyRepository) Update(k *models.APIKey) error {
	return r.db.Save(k).Error
}

// Includes revoked/expired keys so the caller can constant-time compare and report validity precisely.
func (r *APIKeyRepository) FindByPrefix(prefix string) ([]models.APIKey, error) {
	var keys []models.APIKey
	if err := r.db.Where("key_prefix = ?", prefix).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

func (r *APIKeyRepository) FindByID(id uint) (*models.APIKey, error) {
	var k models.APIKey
	if err := r.db.First(&k, id).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

// Ephemeral per-run job/registry keys are excluded so they never appear in the API-keys UI.
func (r *APIKeyRepository) ListByUser(userID uint) ([]models.APIKey, error) {
	var keys []models.APIKey
	err := r.db.Where("user_id = ? AND ephemeral = ?", userID, false).Order("created_at DESC").Find(&keys).Error
	return keys, err
}

func (r *APIKeyRepository) DeleteExpiredEphemeral(now time.Time) (int, error) {
	res := r.db.Where("ephemeral = ? AND expires_at IS NOT NULL AND expires_at < ?", true, now).
		Delete(&models.APIKey{})
	return int(res.RowsAffected), res.Error
}

// RevokeAllByUser revokes every key a user holds, ephemeral ones included.
func (r *APIKeyRepository) RevokeAllByUser(userID uint) error {
	return r.db.Model(&models.APIKey{}).Where("user_id = ? AND revoked = ?", userID, false).Update("revoked", true).Error
}

func (r *APIKeyRepository) Revoke(id uint) error {
	return r.db.Model(&models.APIKey{}).Where("id = ?", id).Update("revoked", true).Error
}

// Handlers only allow this for keys already revoked or expired.
func (r *APIKeyRepository) Delete(id uint) error {
	return r.db.Delete(&models.APIKey{}, id).Error
}

func (r *APIKeyRepository) TouchLastUsed(id uint) error {
	return r.db.Model(&models.APIKey{}).Where("id = ?", id).Update("last_used_at", gorm.Expr("NOW()")).Error
}

// Scopes are a JSON column, so the match is on the quoted name.
func (r *APIKeyRepository) ListByScope(scope string, workspaceID *uint) ([]models.APIKey, error) {
	var out []models.APIKey
	q := r.db.Where("scopes LIKE ?", `%"`+scope+`"%`)
	if workspaceID != nil {
		q = q.Where("workspace_id = ?", *workspaceID)
	} else {
		q = q.Where("workspace_id IS NULL")
	}
	return out, q.Order("created_at DESC").Find(&out).Error
}
