// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type DeploymentRepository struct {
	db *gorm.DB
}

func NewDeploymentRepository(db *gorm.DB) *DeploymentRepository {
	return &DeploymentRepository{db: db}
}

func (r *DeploymentRepository) Create(d *models.Deployment) error { return r.db.Create(d).Error }

// Update saves a deployment WITHOUT its log columns: those are managed out-of-band by AppendLog
// and SetLogMeta, so the in-memory d.Logs is a stale "" here and a plain Save would wipe the
// accumulated build/deploy log the log store later reads.
func (r *DeploymentRepository) Update(d *models.Deployment) error {
	return r.db.Omit("logs", "log_ref", "log_bytes", "log_lines", "log_truncated").Save(d).Error
}

func (r *DeploymentRepository) FindByID(id uint) (*models.Deployment, error) {
	var d models.Deployment
	if err := r.db.First(&d, id).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DeploymentRepository) ListByApp(appID uint, limit int) ([]models.Deployment, error) {
	var deployments []models.Deployment
	q := r.db.Where("application_id = ?", appID).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&deployments).Error
	return deployments, err
}

// LatestNumberByApp returns the highest deployment Number for an app (0 when it
// has none). A pending deploy whose Number is below this has been superseded by a
// newer one, so it should step aside rather than keep waiting.
func (r *DeploymentRepository) LatestNumberByApp(appID uint) (int, error) {
	var n int
	err := r.db.Model(&models.Deployment{}).
		Where("application_id = ?", appID).
		Select("COALESCE(MAX(number), 0)").Scan(&n).Error
	return n, err
}

// AppendLog appends a line to the deployment's stored log tail.
func (r *DeploymentRepository) AppendLog(id uint, line string) error {
	return r.db.Model(&models.Deployment{}).Where("id = ?", id).
		Update("logs", gorm.Expr("COALESCE(logs, '') || ?", line+"\n")).Error
}

// SetLogMeta records the log-store reference + counters for a deployment and
// replaces the DB column with the bounded tail (the full log now lives in the
// store). A zero ref is ignored so a store failure leaves the full DB tail intact.
func (r *DeploymentRepository) SetLogMeta(id uint, ref, tail string, bytes int64, lines int, truncated bool) error {
	if ref == "" {
		return nil
	}
	return r.db.Model(&models.Deployment{}).Where("id = ?", id).
		Updates(map[string]any{
			"logs":          tail,
			"log_ref":       ref,
			"log_bytes":     bytes,
			"log_lines":     lines,
			"log_truncated": truncated,
		}).Error
}
