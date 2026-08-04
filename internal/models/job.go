// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// JobStatus is the lifecycle state of a one-off Job run.
type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCanceled  JobStatus = "canceled"
)

// IsTerminal reports whether the job has reached a final state.
func (s JobStatus) IsTerminal() bool {
	return s == JobSucceeded || s == JobFailed || s == JobCanceled
}

// Job sources.
const (
	JobSourceManual    = "manual"
	JobSourceAPI       = "api"
	JobSourceScheduled = "scheduled"
)

// Job is a single one-off command run in an application's runtime context (same
// image, env, networks, volumes, node, and resource limits as the app). The
// command is captured for history/audit; secrets are resolved into the container
// env at run time and never persisted on the row.
type Job struct {
	ID            uint `json:"id" gorm:"primaryKey"`
	WorkspaceID   uint `json:"workspace_id" gorm:"index;not null"`
	ApplicationID uint `json:"application_id" gorm:"index;not null"`
	// ServerID is the node the job ran on (0 = local), copied from the app.
	ServerID uint `json:"server_id" gorm:"not null;default:0"`
	// CronJobID links runs spawned by a CronJob to their schedule.
	CronJobID *uint `json:"cronjob_id,omitempty" gorm:"index"`
	// AppName is the owning application's name (transient; populated on read for
	// the workspace-level Jobs view).
	AppName string `json:"app_name,omitempty" gorm:"-"`

	Name       string   `json:"name"`
	Command    []string `json:"command" gorm:"serializer:json"`
	Entrypoint []string `json:"entrypoint,omitempty" gorm:"serializer:json"`
	Image      string   `json:"image"` // resolved image actually run
	// RegistryID authenticates the image pull for a custom image (nil = the app's
	// registry, or anonymous). Pull marks that the image must be pulled before the
	// run (set for a custom image; the app's active-release image is already
	// present on its node, and git-built images are local-only).
	RegistryID  *uint     `json:"registry_id,omitempty"`
	Pull        bool      `json:"pull"`
	Status      JobStatus `json:"status" gorm:"not null;default:pending"`
	ExitCode    *int      `json:"exit_code,omitempty"`
	ContainerID string    `json:"container_id,omitempty"`

	Logs         string `json:"logs,omitempty" gorm:"type:text"`
	LogRef       string `json:"log_ref,omitempty"`
	LogBytes     int64  `json:"log_bytes,omitempty"`
	LogLines     int    `json:"log_lines,omitempty"`
	LogTruncated bool   `json:"log_truncated,omitempty"`
	Error        string `json:"error,omitempty" gorm:"type:text"`
	TimeoutSecs  int    `json:"timeout_secs" gorm:"not null;default:0"`

	TriggeredByID *uint  `json:"triggered_by_id,omitempty"`
	Source        string `json:"source" gorm:"not null;default:manual"` // manual | api | scheduled

	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// CronJob concurrency policies (Kubernetes parity): what to do when a tick fires
// while the previous run is still active.
const (
	ConcurrencyAllow   = "allow"   // always spawn a new Job
	ConcurrencyForbid  = "forbid"  // skip the tick if a run is still active
	ConcurrencyReplace = "replace" // cancel the in-flight run, then spawn
)

// CronJob is a schedule that spawns Jobs on a cron expression — the command it
// carries is the template each spawned Job is created from. Schedules are
// evaluated in UTC; missed ticks (control plane down) are not backfilled.
type CronJob struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID   uint   `json:"workspace_id" gorm:"index;not null"`
	ApplicationID uint   `json:"application_id" gorm:"index;not null"`
	Name          string `json:"name" gorm:"not null"`
	Schedule      string `json:"schedule" gorm:"not null"` // cron expression (UTC)
	// AppName is the owning application's name (transient; populated on read).
	AppName string `json:"app_name,omitempty" gorm:"-"`

	Command     []string `json:"command" gorm:"serializer:json"`
	Entrypoint  []string `json:"entrypoint,omitempty" gorm:"serializer:json"`
	TimeoutSecs int      `json:"timeout_secs" gorm:"not null;default:0"`
	// Image optionally overrides the app's active-release image for spawned jobs
	// (blank = run the app's current image). RegistryID authenticates its pull.
	Image      string `json:"image,omitempty"`
	RegistryID *uint  `json:"registry_id,omitempty"`

	Enabled           bool   `json:"enabled" gorm:"not null;default:true"`
	ConcurrencyPolicy string `json:"concurrency_policy" gorm:"not null;default:allow"`
	HistoryLimit      int    `json:"history_limit" gorm:"not null;default:0"` // keep last N spawned jobs (0 = default)

	LastRunAt *time.Time `json:"last_run_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}
