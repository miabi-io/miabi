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

type SessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) *SessionRepository { return &SessionRepository{db: db} }

func (r *SessionRepository) Create(s *models.Session) error {
	return r.db.Create(s).Error
}

func (r *SessionRepository) FindByJTI(jti string) (*models.Session, error) {
	var s models.Session
	if err := r.db.Where("jti = ?", jti).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SessionRepository) RevokeByJTI(jti string) error {
	return r.db.Model(&models.Session{}).Where("jti = ?", jti).Update("revoked", true).Error
}

// RevokeAllForUser marks every session of a user revoked, except the one with exceptJTI (empty for all).
func (r *SessionRepository) RevokeAllForUser(userID uint, exceptJTI string) error {
	q := r.db.Model(&models.Session{}).Where("user_id = ? AND revoked = ?", userID, false)
	if exceptJTI != "" {
		q = q.Where("jti <> ?", exceptJTI)
	}
	return q.Update("revoked", true).Error
}

func (r *SessionRepository) ListByUser(userID uint) ([]models.Session, error) {
	var sessions []models.Session
	err := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&sessions).Error
	return sessions, err
}

func (r *SessionRepository) FindByIDForUser(id, userID uint) (*models.Session, error) {
	var s models.Session
	if err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}
