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
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PipelineRepository persists pipeline definitions, runs, and step runs.
type PipelineRepository struct {
	db *gorm.DB
}

func NewPipelineRepository(db *gorm.DB) *PipelineRepository { return &PipelineRepository{db: db} }

func (r *PipelineRepository) Create(p *models.PipelineDefinition) error { return r.db.Create(p).Error }
func (r *PipelineRepository) Update(p *models.PipelineDefinition) error { return r.db.Save(p).Error }
func (r *PipelineRepository) Delete(workspaceID, id uint) error {
	return r.db.Where("workspace_id = ?", workspaceID).Delete(&models.PipelineDefinition{}, id).Error
}

func (r *PipelineRepository) FindInWorkspace(workspaceID, id uint) (*models.PipelineDefinition, error) {
	var p models.PipelineDefinition
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// FindByID loads a pipeline definition without workspace scoping (scheduler use).
func (r *PipelineRepository) FindByID(id uint) (*models.PipelineDefinition, error) {
	var p models.PipelineDefinition
	if err := r.db.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *PipelineRepository) ListByWorkspace(workspaceID uint) ([]models.PipelineDefinition, error) {
	var out []models.PipelineDefinition
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&out).Error
	return out, err
}

func (r *PipelineRepository) ListByWorkspacePaged(workspaceID uint, limit, offset int) ([]models.PipelineDefinition, int64, error) {
	var (
		out   []models.PipelineDefinition
		total int64
	)
	q := r.db.Model(&models.PipelineDefinition{}).Where("workspace_id = ?", workspaceID)
	q.Count(&total)
	if err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *PipelineRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.PipelineDefinition{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

func (r *PipelineRepository) CreateRun(run *models.PipelineRun) error { return r.db.Create(run).Error }
func (r *PipelineRepository) UpdateRun(run *models.PipelineRun) error { return r.db.Save(run).Error }

func (r *PipelineRepository) SetRunImage(runID, imageID uint) error {
	return r.db.Model(&models.PipelineRun{}).Where("id = ?", runID).Update("image_id", imageID).Error
}

// SetRunCommit pins the resolved source commit a run was built from.
func (r *PipelineRepository) SetRunCommit(runID uint, commit string) error {
	return r.db.Model(&models.PipelineRun{}).Where("id = ?", runID).Update("commit", commit).Error
}

func (r *PipelineRepository) ListEnabledByApp(appID uint) ([]models.PipelineDefinition, error) {
	var out []models.PipelineDefinition
	err := r.db.Where("application_id = ? AND enabled = ?", appID, true).Find(&out).Error
	return out, err
}

func (r *PipelineRepository) ListByApp(appID uint) ([]models.PipelineDefinition, error) {
	var out []models.PipelineDefinition
	err := r.db.Where("application_id = ?", appID).Find(&out).Error
	return out, err
}

// ListEnabled returns every enabled pipeline definition (cron registration sweep).
func (r *PipelineRepository) ListEnabled() ([]models.PipelineDefinition, error) {
	var out []models.PipelineDefinition
	err := r.db.Where("enabled = ?", true).Find(&out).Error
	return out, err
}

// DeleteRun removes a run that was refused before it was queued.
func (r *PipelineRepository) DeleteRun(id uint) error {
	return r.db.Delete(&models.PipelineRun{}, id).Error
}

// The pipeline row is locked so two triggers can't read the same MAX and share a number.
func (r *PipelineRepository) CreateNumberedRun(run *models.PipelineRun) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var def models.PipelineDefinition
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&def, run.PipelineID).Error; err != nil {
			return err
		}
		var max struct{ N int }
		if err := tx.Model(&models.PipelineRun{}).Select("COALESCE(MAX(number),0) AS n").
			Where("pipeline_id = ?", run.PipelineID).Scan(&max).Error; err != nil {
			return err
		}
		run.Number = max.N + 1
		return tx.Create(run).Error
	})
}

func (r *PipelineRepository) FindRun(workspaceID, id uint) (*models.PipelineRun, error) {
	var run models.PipelineRun
	if err := r.db.Preload("Steps", func(db *gorm.DB) *gorm.DB {
		return db.Order("ordinal ASC")
	}).Where("id = ? AND workspace_id = ?", id, workspaceID).First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *PipelineRepository) RunDeployment(workspaceID, runID uint) (*models.RunDeployment, error) {
	var rows []models.RunDeployment
	err := r.db.Table("deployments").
		Select("deployments.id, deployments.number, deployments.status, deployments.application_id, applications.name AS application_name").
		Joins("JOIN applications ON applications.id = deployments.application_id").
		Where("deployments.pipeline_run_id = ? AND applications.workspace_id = ?", runID, workspaceID).
		Order("deployments.id DESC").Limit(1).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// FindRunByID loads a run without workspace scoping (worker use).
func (r *PipelineRepository) FindRunByID(id uint) (*models.PipelineRun, error) {
	var run models.PipelineRun
	if err := r.db.Preload("Steps", func(db *gorm.DB) *gorm.DB {
		return db.Order("ordinal ASC")
	}).First(&run, id).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *PipelineRepository) ListRunsPaged(workspaceID, pipelineID uint, limit, offset int) ([]models.PipelineRun, int64, error) {
	var (
		out   []models.PipelineRun
		total int64
	)
	q := r.db.Model(&models.PipelineRun{}).Where("workspace_id = ? AND pipeline_id = ?", workspaceID, pipelineID)
	q.Count(&total)
	if err := q.Order("number DESC").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Correlated subquery keeps it portable across Postgres and SQLite (no window functions / DISTINCT ON).
func (r *PipelineRepository) LatestRunByPipeline(pipelineIDs []uint) (map[uint]models.PipelineRun, error) {
	out := make(map[uint]models.PipelineRun, len(pipelineIDs))
	if len(pipelineIDs) == 0 {
		return out, nil
	}
	var runs []models.PipelineRun
	err := r.db.
		Where("pipeline_id IN ?", pipelineIDs).
		Where("number = (SELECT MAX(number) FROM pipeline_runs t WHERE t.pipeline_id = pipeline_runs.pipeline_id)").
		Find(&runs).Error
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		out[run.PipelineID] = run
	}
	return out, nil
}

func (r *PipelineRepository) ListRuns(workspaceID, pipelineID uint, limit int) ([]models.PipelineRun, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []models.PipelineRun
	err := r.db.Where("workspace_id = ? AND pipeline_id = ?", workspaceID, pipelineID).
		Order("number DESC").Limit(limit).Find(&out).Error
	return out, err
}

func (r *PipelineRepository) RunBuilds(runID uint) (bool, error) {
	var n int64
	err := r.db.Model(&models.PipelineStepRun{}).Where("pipeline_run_id = ? AND uses = ?", runID, "build").Count(&n).Error
	return n > 0, err
}

func (r *PipelineRepository) CreateStep(s *models.PipelineStepRun) error { return r.db.Create(s).Error }
func (r *PipelineRepository) UpdateStep(s *models.PipelineStepRun) error { return r.db.Save(s).Error }

// A zero ref is ignored so a store failure leaves the full DB tail intact.
func (r *PipelineRepository) SetStepLogMeta(id uint, ref, tail string, bytes int64, lines int, truncated bool) error {
	if ref == "" {
		return nil
	}
	return r.db.Model(&models.PipelineStepRun{}).Where("id = ?", id).
		Updates(map[string]any{
			"logs":          tail,
			"log_ref":       ref,
			"log_bytes":     bytes,
			"log_lines":     lines,
			"log_truncated": truncated,
		}).Error
}

// IDByUID resolves a pipeline's uid to its numeric id.
func (r *PipelineRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.PipelineDefinition](r.db, uid)
}

// An all-digit ref is tried as an id first, then as a name, since all-digit names are valid.
func (r *PipelineRepository) IDByRef(workspaceID uint, ref string) (uint, error) {
	ref = strings.TrimSpace(ref)
	find := func(where string, arg any) uint {
		var got uint
		r.db.Model(&models.PipelineDefinition{}).Where("workspace_id = ? AND "+where, workspaceID, arg).Limit(1).Pluck("id", &got)
		return got
	}
	var id uint
	if n, err := strconv.ParseUint(ref, 10, 64); err == nil && n > 0 {
		id = find("id = ?", n)
	}
	if id == 0 {
		if _, err := uuid.Parse(ref); err == nil {
			id = find("uid = ?", ref)
		}
	}
	if id == 0 && ref != "" {
		id = find("name = ?", strings.ToLower(ref))
	}
	if id == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	return id, nil
}
