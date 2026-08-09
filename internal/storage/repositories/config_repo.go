// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// ConfigRepository persists workspace-scoped configuration files.
type ConfigRepository struct {
	db *gorm.DB
}

func NewConfigRepository(db *gorm.DB) *ConfigRepository { return &ConfigRepository{db: db} }

func (r *ConfigRepository) Create(c *models.Config) error { return r.db.Create(c).Error }
func (r *ConfigRepository) Update(c *models.Config) error { return r.db.Save(c).Error }

func (r *ConfigRepository) FindInWorkspace(workspaceID, id uint) (*models.Config, error) {
	var c models.Config
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *ConfigRepository) FindByName(workspaceID uint, name string) (*models.Config, error) {
	var c models.Config
	if err := r.db.Where("workspace_id = ? AND name = ?", workspaceID, name).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *ConfigRepository) ListByWorkspace(workspaceID uint) ([]models.Config, error) {
	var configs []models.Config
	err := r.db.Where("workspace_id = ?", workspaceID).Order("name ASC").Find(&configs).Error
	return configs, err
}

// ListByWorkspacePaged mirrors the secret repository: ownership and search are
// filtered in SQL because the result is paged, so a caller-side filter would hide
// rows the total count still claims.
func (r *ConfigRepository) ListByWorkspacePaged(workspaceID uint, search string, managed *bool, limit, offset int) ([]models.Config, int64, error) {
	var (
		configs []models.Config
		total   int64
	)
	q := r.db.Model(&models.Config{}).Where("workspace_id = ?", workspaceID)
	if s := strings.TrimSpace(search); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	if managed != nil {
		q = q.Where("managed = ?", *managed)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := q.Order("name ASC").Limit(limit).Offset(offset).Find(&configs).Error
	return configs, total, err
}

func (r *ConfigRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var n int64
	err := r.db.Model(&models.Config{}).Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&n).Error
	return n > 0, err
}

func (r *ConfigRepository) ListByOwner(workspaceID uint, ownerKind string, ownerID uint) ([]models.Config, error) {
	var configs []models.Config
	err := r.db.Where("workspace_id = ? AND owner_kind = ? AND owner_id = ?", workspaceID, ownerKind, ownerID).Find(&configs).Error
	return configs, err
}

func (r *ConfigRepository) Delete(id uint) error {
	return r.db.Delete(&models.Config{}, id).Error
}

func (r *ConfigRepository) IDByUID(uid string) (uint, error) {
	var c models.Config
	if err := r.db.Select("id").Where("uid = ?", uid).First(&c).Error; err != nil {
		return 0, err
	}
	return c.ID, nil
}
