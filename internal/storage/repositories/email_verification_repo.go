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

type EmailVerificationRepository struct {
	db *gorm.DB
}

func NewEmailVerificationRepository(db *gorm.DB) *EmailVerificationRepository {
	return &EmailVerificationRepository{db: db}
}

func (r *EmailVerificationRepository) Create(t *models.EmailVerificationToken) error {
	return r.db.Create(t).Error
}

// FindValidByHash returns an unused, unexpired token by hash.
func (r *EmailVerificationRepository) FindValidByHash(hash string) (*models.EmailVerificationToken, error) {
	var t models.EmailVerificationToken
	err := r.db.Where("token_hash = ? AND used_at IS NULL AND expires_at > ?", hash, time.Now()).
		First(&t).Error
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *EmailVerificationRepository) MarkUsed(id uint) error {
	return r.db.Model(&models.EmailVerificationToken{}).
		Where("id = ?", id).Update("used_at", gorm.Expr("NOW()")).Error
}

// Re-sending retires the previous link rather than leaving several live.
func (r *EmailVerificationRepository) InvalidateForUser(userID uint) error {
	return r.db.Model(&models.EmailVerificationToken{}).
		Where("user_id = ? AND used_at IS NULL", userID).
		Update("used_at", gorm.Expr("NOW()")).Error
}
