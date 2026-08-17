// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/job"
)

// JobHandler exposes one-off Jobs and CronJobs scoped to an app.
type JobHandler struct {
	svc   *job.Service
	audit *audit.Logger
	logs  *logstore.Store
}

func NewJobHandler(svc *job.Service, auditLog *audit.Logger) *JobHandler {
	return &JobHandler{svc: svc, audit: auditLog}
}

// SetLogStore wires the shared execution-log store so a job's full output can be
// downloaded from the store (falling back to the DB tail). nil keeps tail-only.
func (h *JobHandler) SetLogStore(s *logstore.Store) { h.logs = s }

// LogsDownload streams a job's full captured output as a file download.
func (h *JobHandler) LogsDownload(c *okapi.Context) error {
	j, err := h.svc.Get(middlewares.WorkspaceID(c), h.jobID(c))
	if err != nil {
		return c.AbortNotFound("job not found")
	}
	return streamLogDownload(c, h.logs, j.LogRef, j.Logs, "job-"+strconv.FormatUint(uint64(j.ID), 10)+".log")
}

type RunJobRequest struct {
	Body struct {
		ApplicationID uint     `json:"application_id" required:"true"`
		Name          string   `json:"name"`
		Command       []string `json:"command" required:"true"`
		Entrypoint    []string `json:"entrypoint"`
		// Image optionally overrides the app's active-release image (custom image
		// run in the app's runtime). RegistryID authenticates its pull.
		Image      string `json:"image"`
		RegistryID *uint  `json:"registry_id"`
		// RunAsUser pins this run to an account ("1000", "1000:1000", "node"), like
		// `docker run --user`. Empty inherits the app's; a workspace under the
		// restricted security profile must give a non-root numeric uid.
		RunAsUser   string `json:"run_as_user"`
		TimeoutSecs int    `json:"timeout_secs"`
	} `json:"body"`
}

// Run creates and enqueues a one-off job in the target app's runtime context.
func (h *JobHandler) Run(c *okapi.Context, req *RunJobRequest) error {
	wsID := middlewares.WorkspaceID(c)
	actor := middlewares.UserID(c)
	j, err := h.svc.Run(c.Request().Context(), wsID, req.Body.ApplicationID, job.RunRequest{
		Name:        req.Body.Name,
		Command:     req.Body.Command,
		Entrypoint:  req.Body.Entrypoint,
		Image:       req.Body.Image,
		RegistryID:  req.Body.RegistryID,
		RunAsUser:   req.Body.RunAsUser,
		TimeoutSecs: req.Body.TimeoutSecs,
		Source:      "manual",
		TriggeredBy: &actor,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "job.create", j.ID)
	return created(c, j)
}

// List returns the workspace's job history, optionally filtered to ?app_id.
func (h *JobHandler) List(c *okapi.Context) error {
	jobs, err := h.svc.List(middlewares.WorkspaceID(c), queryUint(c, "app_id"), 200)
	if err != nil {
		return c.AbortInternalServerError("failed to list jobs", err)
	}
	return ok(c, jobs)
}

func (h *JobHandler) Get(c *okapi.Context) error {
	j, err := h.svc.Get(middlewares.WorkspaceID(c), h.jobID(c))
	if err != nil {
		return c.AbortNotFound("job not found")
	}
	return ok(c, j)
}

func (h *JobHandler) Cancel(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Cancel(c.Request().Context(), wsID, h.jobID(c)); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "job.cancel", h.jobID(c))
	return message(c, "job canceled")
}

func (h *JobHandler) Delete(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Delete(wsID, h.jobID(c)); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "job.delete", h.jobID(c))
	return message(c, "job deleted")
}

type CronJobRequest struct {
	Body struct {
		ApplicationID uint     `json:"application_id"`
		Name          string   `json:"name"`
		Schedule      string   `json:"schedule" required:"true"`
		Command       []string `json:"command" required:"true"`
		Entrypoint    []string `json:"entrypoint"`
		Image         string   `json:"image"`
		RegistryID    *uint    `json:"registry_id"`
		// RunAsUser pins spawned runs to an account; empty inherits the app's when
		// each run fires. See RunJobRequest.
		RunAsUser         string `json:"run_as_user"`
		TimeoutSecs       int    `json:"timeout_secs"`
		Enabled           bool   `json:"enabled"`
		ConcurrencyPolicy string `json:"concurrency_policy" enum:"allow,forbid,replace"`
		HistoryLimit      int    `json:"history_limit"`
	} `json:"body"`
}

func (r *CronJobRequest) input() job.CronJobInput {
	return job.CronJobInput{
		Name:              r.Body.Name,
		Schedule:          r.Body.Schedule,
		Command:           r.Body.Command,
		Entrypoint:        r.Body.Entrypoint,
		Image:             r.Body.Image,
		RegistryID:        r.Body.RegistryID,
		RunAsUser:         r.Body.RunAsUser,
		TimeoutSecs:       r.Body.TimeoutSecs,
		Enabled:           r.Body.Enabled,
		ConcurrencyPolicy: r.Body.ConcurrencyPolicy,
		HistoryLimit:      r.Body.HistoryLimit,
	}
}

func (h *JobHandler) ListCronJobs(c *okapi.Context) error {
	list, err := h.svc.ListCronJobs(middlewares.WorkspaceID(c), queryUint(c, "app_id"))
	if err != nil {
		return c.AbortInternalServerError("failed to list cronjobs", err)
	}
	return ok(c, list)
}

func (h *JobHandler) CreateCronJob(c *okapi.Context, req *CronJobRequest) error {
	if req.Body.ApplicationID == 0 {
		return c.AbortBadRequest("application_id is required")
	}
	wsID := middlewares.WorkspaceID(c)
	cj, err := h.svc.CreateCronJob(wsID, req.Body.ApplicationID, req.input())
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "cronjob.create", cj.ID)
	return created(c, cj)
}

func (h *JobHandler) GetCronJob(c *okapi.Context) error {
	cj, err := h.svc.GetCronJob(middlewares.WorkspaceID(c), h.cronJobID(c))
	if err != nil {
		return c.AbortNotFound("cronjob not found")
	}
	return ok(c, cj)
}

func (h *JobHandler) UpdateCronJob(c *okapi.Context, req *CronJobRequest) error {
	wsID := middlewares.WorkspaceID(c)
	cj, err := h.svc.UpdateCronJob(wsID, h.cronJobID(c), req.input())
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "cronjob.update", cj.ID)
	return ok(c, cj)
}

func (h *JobHandler) RunCronJobNow(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	actor := middlewares.UserID(c)
	j, err := h.svc.RunCronJobNow(c.Request().Context(), wsID, h.cronJobID(c), &actor)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "cronjob.run", h.cronJobID(c))
	return created(c, j)
}

func (h *JobHandler) DeleteCronJob(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.DeleteCronJob(wsID, h.cronJobID(c)); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "cronjob.delete", h.cronJobID(c))
	return message(c, "cronjob deleted")
}

func (h *JobHandler) jobID(c *okapi.Context) uint {
	id, _ := strconv.Atoi(c.Param("jobID"))
	return uint(id)
}

func (h *JobHandler) cronJobID(c *okapi.Context) uint {
	id, _ := strconv.Atoi(c.Param("cronJobID"))
	return uint(id)
}

func queryUint(c *okapi.Context, key string) uint {
	id, _ := strconv.Atoi(c.Query(key))
	if id < 0 {
		return 0
	}
	return uint(id)
}

func (h *JobHandler) mapErr(c *okapi.Context, err error) error {
	if a := quotaAbort(c, err); a != nil {
		return a
	}
	switch {
	case errors.Is(err, job.ErrNotFound):
		return c.AbortNotFound("job not found")
	case errors.Is(err, job.ErrNoCommand):
		return c.AbortBadRequest("a command is required")
	case errors.Is(err, job.ErrNoImage):
		return c.AbortBadRequest("application has no image yet; deploy it first")
	case errors.Is(err, models.ErrRunAsUserInvalid), errors.Is(err, models.ErrRunAsUserRoot):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, job.ErrInvalidSchedule):
		return c.AbortBadRequest("invalid cron schedule")
	case errors.Is(err, job.ErrCronNotFound):
		return c.AbortNotFound("cronjob not found")
	case errors.Is(err, job.ErrNotTerminal):
		return c.AbortWithError(409, err)
	case errors.Is(err, job.ErrAlreadyDone):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("job operation failed", err)
	}
}

func (h *JobHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "job", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
