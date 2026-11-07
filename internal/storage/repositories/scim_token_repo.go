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
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// HashSCIMToken is shared by the admin mint path and the enterprise SCIM authenticator so they agree.
func HashSCIMToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// SCIMTokenRepository accesses SCIM bearer credentials (Enterprise; empty in CE).
type SCIMTokenRepository struct{ db *gorm.DB }

func NewSCIMTokenRepository(db *gorm.DB) *SCIMTokenRepository {
	return &SCIMTokenRepository{db: db}
}

func (r *SCIMTokenRepository) Create(t *models.SCIMToken) error { return r.db.Create(t).Error }
func (r *SCIMTokenRepository) Delete(id uint) error {
	return r.db.Delete(&models.SCIMToken{}, id).Error
}

func (r *SCIMTokenRepository) FindAll() ([]models.SCIMToken, error) {
	var tokens []models.SCIMToken
	err := r.db.Order("created_at DESC").Find(&tokens).Error
	return tokens, err
}

// FindByHash returns the token matching a hashed bearer value.
func (r *SCIMTokenRepository) FindByHash(hash string) (*models.SCIMToken, error) {
	var t models.SCIMToken
	if err := r.db.Where("token_hash = ?", hash).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// TouchLastUsed records that a token authenticated a request (best-effort).
func (r *SCIMTokenRepository) TouchLastUsed(id uint) {
	now := time.Now()
	_ = r.db.Model(&models.SCIMToken{}).Where("id = ?", id).Update("last_used_at", now).Error
}
