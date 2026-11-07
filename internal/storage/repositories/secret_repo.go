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
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// SecretRepository persists workspace-scoped secrets (the Vault).
type SecretRepository struct {
	db *gorm.DB
}

func NewSecretRepository(db *gorm.DB) *SecretRepository { return &SecretRepository{db: db} }

func (r *SecretRepository) Create(s *models.Secret) error { return r.db.Create(s).Error }
func (r *SecretRepository) Update(s *models.Secret) error { return r.db.Save(s).Error }

func (r *SecretRepository) FindInWorkspace(workspaceID, id uint) (*models.Secret, error) {
	var s models.Secret
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SecretRepository) FindByName(workspaceID uint, name string) (*models.Secret, error) {
	var s models.Secret
	if err := r.db.Where("workspace_id = ? AND name = ?", workspaceID, name).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SecretRepository) CountSealedByKeyVersion(workspaceID uint) (map[int]int, error) {
	var rows []struct {
		SealedKeyVersion int
		N                int
	}
	err := r.db.Model(&models.Secret{}).
		Select("sealed_key_version, COUNT(*) AS n").
		Where("workspace_id = ? AND sealed_key_version > 0", workspaceID).
		Group("sealed_key_version").Scan(&rows).Error
	out := make(map[int]int, len(rows))
	for _, r := range rows {
		out[r.SealedKeyVersion] = r.N
	}
	return out, err
}

func (r *SecretRepository) ListByWorkspace(workspaceID uint) ([]models.Secret, error) {
	var secrets []models.Secret
	err := r.db.Where("workspace_id = ?", workspaceID).Order("name ASC").Find(&secrets).Error
	return secrets, err
}

// Ownership is filtered here because the result is paged: a client-side filter would hide rows the count claims.
func (r *SecretRepository) ListByWorkspacePaged(workspaceID uint, search string, managed *bool, limit, offset int) ([]models.Secret, int64, error) {
	var (
		secrets []models.Secret
		total   int64
	)
	q := r.db.Model(&models.Secret{}).Where("workspace_id = ?", workspaceID)
	if s := strings.TrimSpace(search); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	if managed != nil {
		q = q.Where("managed = ?", *managed)
	}
	q.Count(&total)
	if err := q.Order("name ASC").Limit(limit).Offset(offset).Find(&secrets).Error; err != nil {
		return nil, 0, err
	}
	return secrets, total, nil
}

func (r *SecretRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Secret{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

// ListByOwner returns the managed secrets owned by a platform resource.
func (r *SecretRepository) ListByOwner(workspaceID uint, ownerKind string, ownerID uint) ([]models.Secret, error) {
	var secrets []models.Secret
	err := r.db.Where("workspace_id = ? AND owner_kind = ? AND owner_id = ?", workspaceID, ownerKind, ownerID).
		Find(&secrets).Error
	return secrets, err
}

func (r *SecretRepository) Delete(id uint) error {
	return r.db.Delete(&models.Secret{}, id).Error
}

// IDByUID resolves a secret's uid to its numeric id.
func (r *SecretRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.Secret](r.db, uid)
}
