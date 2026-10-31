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

// ImageRepository persists the built-image catalog.
type ImageRepository struct {
	db *gorm.DB
}

func NewImageRepository(db *gorm.DB) *ImageRepository { return &ImageRepository{db: db} }

func (r *ImageRepository) Create(i *models.Image) error { return r.db.Create(i).Error }
func (r *ImageRepository) Update(i *models.Image) error { return r.db.Save(i).Error }

// SetTag relabels an image with its human-readable tag.
func (r *ImageRepository) SetTag(id uint, tag string) error {
	return r.db.Model(&models.Image{}).Where("id = ?", id).Update("tag", tag).Error
}
func (r *ImageRepository) Delete(workspaceID, id uint) error {
	return r.db.Where("workspace_id = ?", workspaceID).Delete(&models.Image{}, id).Error
}

func (r *ImageRepository) FindInWorkspace(workspaceID, id uint) (*models.Image, error) {
	var i models.Image
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&i).Error; err != nil {
		return nil, err
	}
	return &i, nil
}

// FindByDigest looks up a workspace image by its immutable digest.
func (r *ImageRepository) FindByDigest(workspaceID uint, digest string) (*models.Image, error) {
	var i models.Image
	if err := r.db.Where("workspace_id = ? AND digest = ?", workspaceID, digest).First(&i).Error; err != nil {
		return nil, err
	}
	return &i, nil
}

// ListByDigests looks up several images at once, for enriching a page of
// registry tags with build provenance without a query per tag.
func (r *ImageRepository) ListByDigests(workspaceID uint, digests []string) ([]models.Image, error) {
	if len(digests) == 0 {
		return nil, nil
	}
	var out []models.Image
	err := r.db.Where("workspace_id = ? AND digest IN ?", workspaceID, digests).Find(&out).Error
	return out, err
}

func (r *ImageRepository) ListByWorkspace(workspaceID uint) ([]models.Image, error) {
	var out []models.Image
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&out).Error
	return out, err
}

func (r *ImageRepository) ListByApp(workspaceID, appID uint) ([]models.Image, error) {
	var out []models.Image
	err := r.db.Where("workspace_id = ? AND application_id = ?", workspaceID, appID).
		Order("created_at DESC").Find(&out).Error
	return out, err
}

// ListAll returns every catalog image across all workspaces, newest first. Used
// by the GC sweep, which applies retention globally.
func (r *ImageRepository) ListAll() ([]models.Image, error) {
	var out []models.Image
	err := r.db.Order("application_id ASC, created_at DESC").Find(&out).Error
	return out, err
}
