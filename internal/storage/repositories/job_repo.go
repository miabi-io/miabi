// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// JobRepository persists one-off Job runs and CronJob schedules.
type JobRepository struct {
	db *gorm.DB
}

func NewJobRepository(db *gorm.DB) *JobRepository { return &JobRepository{db: db} }

func (r *JobRepository) Create(j *models.Job) error { return r.db.Create(j).Error }

// Update saves a job WITHOUT its log columns: those are managed out-of-band by
// AppendLog (raw append during the run) and externalizeLog, so the in-memory
// j.Logs is a stale "" here — a plain Save would wipe the accumulated output.
func (r *JobRepository) Update(j *models.Job) error {
	return r.db.Omit("logs", "log_ref", "log_bytes", "log_lines", "log_truncated").Save(j).Error
}

// FindByID loads a job regardless of workspace (worker use).
func (r *JobRepository) FindByID(id uint) (*models.Job, error) {
	var j models.Job
	if err := r.db.First(&j, id).Error; err != nil {
		return nil, err
	}
	return &j, nil
}

func (r *JobRepository) FindInWorkspace(workspaceID, id uint) (*models.Job, error) {
	var j models.Job
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&j).Error; err != nil {
		return nil, err
	}
	return &j, nil
}

// ListByApp returns an app's job history, newest first.
func (r *JobRepository) ListByApp(workspaceID, appID uint, limit int) ([]models.Job, error) {
	var jobs []models.Job
	q := r.db.Where("workspace_id = ? AND application_id = ?", workspaceID, appID).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&jobs).Error
	return jobs, err
}

// ListByWorkspace returns the whole workspace's job history, newest first. When
// appID is non-zero it filters to that app.
func (r *JobRepository) ListByWorkspace(workspaceID, appID uint, limit int) ([]models.Job, error) {
	var jobs []models.Job
	q := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC")
	if appID > 0 {
		q = q.Where("application_id = ?", appID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&jobs).Error
	return jobs, err
}

// ActiveByCronJob returns the still-running (pending/running) jobs spawned by a
// cronjob — used to enforce its concurrency policy.
func (r *JobRepository) ActiveByCronJob(cronJobID uint) ([]models.Job, error) {
	var jobs []models.Job
	err := r.db.Where("cron_job_id = ? AND status IN ?", cronJobID,
		[]models.JobStatus{models.JobPending, models.JobRunning}).Find(&jobs).Error
	return jobs, err
}

func (r *JobRepository) Delete(id uint) error {
	return r.db.Delete(&models.Job{}, id).Error
}

// AppendLog appends a line to the job's stored log tail.
func (r *JobRepository) AppendLog(id uint, line string) error {
	return r.db.Model(&models.Job{}).Where("id = ?", id).
		Update("logs", gorm.Expr("COALESCE(logs, '') || ?", line+"\n")).Error
}

// SetLogMeta records the log-store reference + counters for a job and replaces
// the DB column with the bounded tail (the full log lives in the store). A zero
// ref is ignored so a store failure leaves the full DB tail intact.
func (r *JobRepository) SetLogMeta(id uint, ref, tail string, bytes int64, lines int, truncated bool) error {
	if ref == "" {
		return nil
	}
	return r.db.Model(&models.Job{}).Where("id = ?", id).
		Updates(map[string]any{
			"logs":          tail,
			"log_ref":       ref,
			"log_bytes":     bytes,
			"log_lines":     lines,
			"log_truncated": truncated,
		}).Error
}

// PruneCronJobHistory deletes the oldest spawned jobs of a cronjob beyond keep,
// counting only terminal runs (never removes an active run).
func (r *JobRepository) PruneCronJobHistory(cronJobID uint, keep int) error {
	if keep <= 0 {
		return nil
	}
	var ids []uint
	if err := r.db.Model(&models.Job{}).
		Where("cron_job_id = ? AND status IN ?", cronJobID,
			[]models.JobStatus{models.JobSucceeded, models.JobFailed, models.JobCanceled}).
		Order("created_at DESC").Offset(keep).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return r.db.Delete(&models.Job{}, ids).Error
}

func (r *JobRepository) CreateCronJob(c *models.CronJob) error { return r.db.Create(c).Error }
func (r *JobRepository) UpdateCronJob(c *models.CronJob) error { return r.db.Save(c).Error }

func (r *JobRepository) FindCronJobByID(id uint) (*models.CronJob, error) {
	var c models.CronJob
	if err := r.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *JobRepository) FindCronJobInWorkspace(workspaceID, id uint) (*models.CronJob, error) {
	var c models.CronJob
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *JobRepository) ListCronJobsByApp(workspaceID, appID uint) ([]models.CronJob, error) {
	var cronJobs []models.CronJob
	err := r.db.Where("workspace_id = ? AND application_id = ?", workspaceID, appID).
		Order("created_at DESC").Find(&cronJobs).Error
	return cronJobs, err
}

// ListCronJobsByWorkspace returns all cronjobs in a workspace, newest first.
func (r *JobRepository) ListCronJobsByWorkspace(workspaceID uint) ([]models.CronJob, error) {
	var cronJobs []models.CronJob
	err := r.db.Where("workspace_id = ?", workspaceID).
		Order("created_at DESC").Find(&cronJobs).Error
	return cronJobs, err
}

// ListEnabledCronJobs returns all enabled schedules (loaded at startup).
func (r *JobRepository) ListEnabledCronJobs() ([]models.CronJob, error) {
	var cronJobs []models.CronJob
	err := r.db.Where("enabled = ?", true).Find(&cronJobs).Error
	return cronJobs, err
}

func (r *JobRepository) DeleteCronJob(id uint) error {
	return r.db.Delete(&models.CronJob{}, id).Error
}
