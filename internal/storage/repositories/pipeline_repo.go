// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
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

// ListByWorkspacePaged returns a page of pipeline definitions plus the total
// count, newest first.
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

// SetRunImage records the artifact a run produced without touching its other
// columns or re-saving step associations.
func (r *PipelineRepository) SetRunImage(runID, imageID uint) error {
	return r.db.Model(&models.PipelineRun{}).Where("id = ?", runID).Update("image_id", imageID).Error
}

// SetRunCommit pins the resolved source commit a run was built from.
func (r *PipelineRepository) SetRunCommit(runID uint, commit string) error {
	return r.db.Model(&models.PipelineRun{}).Where("id = ?", runID).Update("commit", commit).Error
}

// ListEnabledByApp returns the enabled pipeline definitions bound to an app.
// Used to fan a push out to the pipelines that should fire for it.
func (r *PipelineRepository) ListEnabledByApp(appID uint) ([]models.PipelineDefinition, error) {
	var out []models.PipelineDefinition
	err := r.db.Where("application_id = ? AND enabled = ?", appID, true).Find(&out).Error
	return out, err
}

// ListByApp returns every pipeline definition bound to an app, enabled or not.
// Used when the app is deleted, to clean up the repo-owned ones and unbind the
// rest.
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

// NextRunNumber returns the next per-pipeline run counter.
func (r *PipelineRepository) NextRunNumber(pipelineID uint) (int, error) {
	var max struct{ N int }
	err := r.db.Model(&models.PipelineRun{}).
		Select("COALESCE(MAX(number),0) AS n").
		Where("pipeline_id = ?", pipelineID).Scan(&max).Error
	return max.N + 1, err
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

// ListRunsPaged returns a page of a pipeline's runs plus the total count,
// newest run number first.
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

// LatestRunByPipeline returns the most recent run for each of the given pipeline IDs, keyed by
// pipeline ID; pipelines with no runs are absent. Uses a correlated subquery so it stays
// portable across Postgres and SQLite (no window functions / DISTINCT ON).
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

func (r *PipelineRepository) CreateStep(s *models.PipelineStepRun) error { return r.db.Create(s).Error }
func (r *PipelineRepository) UpdateStep(s *models.PipelineStepRun) error { return r.db.Save(s).Error }

// SetStepLogMeta records the log-store reference + counters for a step and
// replaces the DB column with the bounded tail (the full log lives in the
// store). A zero ref is ignored so a store failure leaves the full DB tail intact.
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
