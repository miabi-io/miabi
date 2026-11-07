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
	"errors"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type TemplateRepository struct {
	db *gorm.DB
}

func NewTemplateRepository(db *gorm.DB) *TemplateRepository {
	return &TemplateRepository{db: db}
}

func (r *TemplateRepository) EnsureCustomSource(workspaceID uint) (*models.TemplateSource, error) {
	var src models.TemplateSource
	err := r.db.Where("workspace_id = ? AND type = ?", workspaceID, models.TemplateSourceCustom).First(&src).Error
	if err == nil {
		return &src, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	src = models.TemplateSource{
		WorkspaceID: &workspaceID, Name: "Custom", Type: models.TemplateSourceCustom, SyncStatus: "idle",
	}
	if err := r.db.Create(&src).Error; err != nil {
		return nil, err
	}
	return &src, nil
}

func (r *TemplateRepository) findCustomSource(workspaceID uint) (*models.TemplateSource, error) {
	var src models.TemplateSource
	err := r.db.Where("workspace_id = ? AND type = ?", workspaceID, models.TemplateSourceCustom).First(&src).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &src, err
}

func (r *TemplateRepository) UpsertTemplate(t *models.Template) error {
	var existing models.Template
	err := r.db.Where("source_id = ? AND name = ? AND version = ?", t.SourceID, t.Name, t.Version).First(&existing).Error
	if err == nil {
		t.ID, t.CreatedAt = existing.ID, existing.CreatedAt
		return r.db.Save(t).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return r.db.Create(t).Error
}

// ListCustom returns every custom template version imported into the workspace.
func (r *TemplateRepository) ListCustom(workspaceID uint) ([]models.Template, error) {
	src, err := r.findCustomSource(workspaceID)
	if err != nil || src == nil {
		return nil, err
	}
	var out []models.Template
	err = r.db.Where("source_id = ?", src.ID).Order("name, version DESC").Find(&out).Error
	return out, err
}

// DeleteCustom returns the deleted row count so the caller can tell "not found" (0) from success.
func (r *TemplateRepository) DeleteCustom(workspaceID uint, name string) (int64, error) {
	src, err := r.findCustomSource(workspaceID)
	if err != nil || src == nil {
		return 0, err
	}
	res := r.db.Where("source_id = ? AND name = ?", src.ID, name).Delete(&models.Template{})
	return res.RowsAffected, res.Error
}

// FindCustom returns a custom template by name (empty version = highest).
func (r *TemplateRepository) FindCustom(workspaceID uint, name, version string) (*models.Template, error) {
	src, err := r.findCustomSource(workspaceID)
	if err != nil || src == nil {
		return nil, err
	}
	q := r.db.Where("source_id = ? AND name = ?", src.ID, name)
	if version != "" {
		q = q.Where("version = ?", version)
	}
	var t models.Template
	err = q.Order("version DESC").First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &t, err
}

type TemplateInstallRepository struct {
	db *gorm.DB
}

func NewTemplateInstallRepository(db *gorm.DB) *TemplateInstallRepository {
	return &TemplateInstallRepository{db: db}
}

func (r *TemplateInstallRepository) Create(t *models.TemplateInstall) error {
	return r.db.Create(t).Error
}

func (r *TemplateInstallRepository) ListByWorkspace(workspaceID uint) ([]models.TemplateInstall, error) {
	var out []models.TemplateInstall
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&out).Error
	return out, err
}

func (r *TemplateInstallRepository) FindInWorkspace(workspaceID, id uint) (*models.TemplateInstall, error) {
	var t models.TemplateInstall
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TemplateInstallRepository) Update(t *models.TemplateInstall) error {
	return r.db.Save(t).Error
}

func (r *TemplateInstallRepository) Delete(id uint) error {
	return r.db.Delete(&models.TemplateInstall{}, id).Error
}
