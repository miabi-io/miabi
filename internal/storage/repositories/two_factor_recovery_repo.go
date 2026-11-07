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
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type TwoFactorRecoveryRepository struct {
	db *gorm.DB
}

func NewTwoFactorRecoveryRepository(db *gorm.DB) *TwoFactorRecoveryRepository {
	return &TwoFactorRecoveryRepository{db: db}
}

func (r *TwoFactorRecoveryRepository) ReplaceForUser(userID uint, codes []*models.TwoFactorRecoveryCode) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&models.TwoFactorRecoveryCode{}).Error; err != nil {
			return err
		}
		if len(codes) == 0 {
			return nil
		}
		return tx.Create(&codes).Error
	})
}

// FindUnusedByHash returns an unused recovery code matching the hash for a user.
func (r *TwoFactorRecoveryRepository) FindUnusedByHash(userID uint, hash string) (*models.TwoFactorRecoveryCode, error) {
	var code models.TwoFactorRecoveryCode
	err := r.db.Where("user_id = ? AND code_hash = ? AND used_at IS NULL", userID, hash).First(&code).Error
	if err != nil {
		return nil, err
	}
	return &code, nil
}

// MarkUsed flags a recovery code as consumed.
func (r *TwoFactorRecoveryRepository) MarkUsed(id uint) error {
	return r.db.Model(&models.TwoFactorRecoveryCode{}).
		Where("id = ?", id).Update("used_at", gorm.Expr("NOW()")).Error
}

// CountUnused returns how many unused recovery codes the user has left.
func (r *TwoFactorRecoveryRepository) CountUnused(userID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.TwoFactorRecoveryCode{}).
		Where("user_id = ? AND used_at IS NULL", userID).Count(&n).Error
	return n, err
}

// DeleteForUser removes all of a user's recovery codes.
func (r *TwoFactorRecoveryRepository) DeleteForUser(userID uint) error {
	return r.db.Where("user_id = ?", userID).Delete(&models.TwoFactorRecoveryCode{}).Error
}
