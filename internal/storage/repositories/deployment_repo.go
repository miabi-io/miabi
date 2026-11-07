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

type DeploymentRepository struct {
	db *gorm.DB
}

func NewDeploymentRepository(db *gorm.DB) *DeploymentRepository {
	return &DeploymentRepository{db: db}
}

func (r *DeploymentRepository) Create(d *models.Deployment) error { return r.db.Create(d).Error }

// Omits log columns: they're managed by AppendLog/SetLogMeta, and d.Logs here is a stale "".
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

// A LEFT join, so a machine trigger or deleted user yields an empty name rather than dropping the row.
func (r *DeploymentRepository) ListByAppWithActor(appID uint, limit int) ([]models.DeploymentWithActor, error) {
	var out []models.DeploymentWithActor
	q := r.db.Model(&models.Deployment{}).
		Select("deployments.*, users.name AS triggered_by_name").
		Joins("LEFT JOIN users ON users.id = deployments.triggered_by_id").
		Where("deployments.application_id = ?", appID).
		Order("deployments.created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&out).Error
	return out, err
}

// A deploy that failed recently must not read as "deployed recently" when older code still runs.
func (r *DeploymentRepository) LatestSucceededByApp(appID uint) (*models.DeploymentWithActor, error) {
	var out models.DeploymentWithActor
	err := r.db.Model(&models.Deployment{}).
		Select("deployments.*, users.name AS triggered_by_name").
		Joins("LEFT JOIN users ON users.id = deployments.triggered_by_id").
		Where("deployments.application_id = ? AND deployments.status = ?", appID, models.DeploymentSucceeded).
		Order("deployments.number DESC").
		First(&out).Error
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// A pending deploy numbered below this was superseded and should step aside rather than wait.
func (r *DeploymentRepository) LatestNumberByApp(appID uint) (int, error) {
	var n int
	err := r.db.Model(&models.Deployment{}).
		Where("application_id = ?", appID).
		Select("COALESCE(MAX(number), 0)").Scan(&n).Error
	return n, err
}

// A canary rollout doesn't count: the stable release keeps running throughout.
func (r *DeploymentRepository) InProgressAppIDs() ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.Deployment{}).
		Where("status IN ?", []models.DeploymentStatus{models.DeploymentPending, models.DeploymentBuilding, models.DeploymentDeploying}).
		Distinct().Pluck("application_id", &ids).Error
	return ids, err
}

// AppendLog appends a line to the deployment's stored log tail.
func (r *DeploymentRepository) AppendLog(id uint, line string) error {
	return r.db.Model(&models.Deployment{}).Where("id = ?", id).
		Update("logs", gorm.Expr("COALESCE(logs, '') || ?", line+"\n")).Error
}

// A zero ref is ignored so a store failure leaves the full DB tail intact.
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

func (r *DeploymentRepository) InProgressByApp(appID uint) (*models.Deployment, error) {
	var d models.Deployment
	err := r.db.Where("application_id = ? AND status IN ?", appID,
		[]models.DeploymentStatus{models.DeploymentPending, models.DeploymentBuilding, models.DeploymentDeploying}).
		Order("id DESC").First(&d).Error
	if err != nil {
		return nil, err
	}
	return &d, nil
}
