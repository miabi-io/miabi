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
	// JobSkipped is a run that never started because the deploy it waited for failed.
	JobSkipped JobStatus = "skipped"
)

// TerminalJobStatuses lists the final states, for queries.
var TerminalJobStatuses = []JobStatus{JobSucceeded, JobFailed, JobCanceled, JobSkipped}

// IsTerminal reports whether the job has reached a final state.
func (s JobStatus) IsTerminal() bool {
	return s == JobSucceeded || s == JobFailed || s == JobCanceled || s == JobSkipped
}

// Job sources.
const (
	JobSourceManual    = "manual"
	JobSourceAPI       = "api"
	JobSourceScheduled = "scheduled"
	// JobSourceGitOps is a run of a declared Job started by a sync.
	JobSourceGitOps = "gitops"
	// JobSourceRelease is a run of an onRelease Job started by a release becoming active.
	JobSourceRelease = "release"
)

// Job is a one-off command run in an application's runtime context (same image, env,
// networks, volumes, node and limits). The command is captured for history/audit; secrets are
// resolved into the container env at run time and never persisted on the row.
type Job struct {
	ID            uint `json:"id" gorm:"primaryKey"`
	WorkspaceID   uint `json:"workspace_id" gorm:"index;not null"`
	ApplicationID uint `json:"application_id" gorm:"index;not null"`
	// ServerID is the node the job ran on (0 = local), copied from the app.
	ServerID  uint `json:"server_id" gorm:"not null;default:0"`
	ClusterID uint `json:"cluster_id" gorm:"index;not null;default:0"`
	// CronJobID links runs spawned by a CronJob to their schedule.
	CronJobID *uint `json:"cronjob_id,omitempty" gorm:"index"`
	// JobDefinitionID links runs of a declared Job to their definition; NULL once
	// the definition is deleted, so its history stays.
	JobDefinitionID *uint `json:"job_definition_id,omitempty" gorm:"index"`
	// WaitDeploymentID holds the run until that deployment ends: it starts on the
	// release the deploy activates, or is skipped when the deploy fails.
	WaitDeploymentID *uint `json:"wait_deployment_id,omitempty"`
	// Attempt numbers a declared Job's retries of one run, from 1.
	Attempt int `json:"attempt,omitempty" gorm:"not null;default:1"`
	// AppName is the owning application's name (transient; populated on read for
	// the workspace-level Jobs view).
	AppName string `json:"app_name,omitempty" gorm:"-"`

	Name       string   `json:"name"`
	Command    []string `json:"command" gorm:"serializer:json"`
	Entrypoint []string `json:"entrypoint,omitempty" gorm:"serializer:json"`
	Image      string   `json:"image"` // resolved image actually run
	// RegistryID authenticates the image pull for a custom image (nil = the app's registry, or
	// anonymous). Pull marks that the image must be fetched first — set for a custom image; the
	// app's release image is already on its node and git-built images are local-only.
	RegistryID *uint `json:"registry_id,omitempty"`
	Pull       bool  `json:"pull"`
	// RunAsUser is the account the run was pinned to, snapshotted from the request or the app so
	// history shows what actually ran. Empty = the image's own user (or the platform UID when the
	// restricted profile applies). See models.NormalizeRunAsUser.
	RunAsUser   string    `json:"run_as_user,omitempty"`
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
	UIDModel
	ID            uint `json:"id" gorm:"primaryKey"`
	WorkspaceID   uint `json:"workspace_id" gorm:"index;index:idx_cronjob_workspace_name,unique;not null"`
	ApplicationID uint `json:"application_id" gorm:"index;not null"`
	// Name is the unique slug handle scoped to the workspace; the declarative
	// CronJob/<name> key. DisplayName is the free-text label shown in the UI.
	Name        string `json:"name" gorm:"index:idx_cronjob_workspace_name,unique;not null"`
	DisplayName string `json:"display_name"`
	Schedule    string `json:"schedule" gorm:"not null"` // cron expression (UTC)
	// AppName is the owning application's name (transient; populated on read).
	AppName string `json:"app_name,omitempty" gorm:"-"`

	Command     []string `json:"command" gorm:"serializer:json"`
	Entrypoint  []string `json:"entrypoint,omitempty" gorm:"serializer:json"`
	TimeoutSecs int      `json:"timeout_secs" gorm:"not null;default:0"`
	// Image optionally overrides the app's active-release image for spawned jobs
	// (blank = run the app's current image). RegistryID authenticates its pull.
	Image      string `json:"image,omitempty"`
	RegistryID *uint  `json:"registry_id,omitempty"`
	// RunAsUser pins spawned runs to an account (blank = inherit the app's).
	RunAsUser string `json:"run_as_user,omitempty"`

	Enabled           bool   `json:"enabled" gorm:"not null;default:true"`
	ConcurrencyPolicy string `json:"concurrency_policy" gorm:"not null;default:allow"`
	HistoryLimit      int    `json:"history_limit" gorm:"not null;default:0"` // keep last N spawned jobs (0 = default)

	// Metadata holds labels; "miabi.io/" keys are platform-managed (managed-by,
	// gitops-source). Annotations are free-form descriptive metadata.
	Metadata    Metadata `json:"metadata,omitempty" gorm:"serializer:json"`
	Annotations Metadata `json:"annotations,omitempty" gorm:"serializer:json"`

	LastRunAt *time.Time `json:"last_run_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Job definition run policies (see declarative.JobSpec).
const (
	JobRunOnChange  = "onChange"
	JobRunOnce      = "once"
	JobRunOnRelease = "onRelease"
)

// JobDefinition is a declared Job: a run template bound to an application plus
// the bookkeeping that decides when it runs again. Each run is an ordinary Job
// row pointing back here through JobDefinitionID.
type JobDefinition struct {
	UIDModel
	ID            uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID   uint   `json:"workspace_id" gorm:"index;index:idx_jobdef_workspace_name,unique;not null"`
	ApplicationID uint   `json:"application_id" gorm:"index;not null"`
	Name          string `json:"name" gorm:"index:idx_jobdef_workspace_name,unique;not null"`
	// AppName is the owning application's name (transient; populated on read).
	AppName string `json:"app_name,omitempty" gorm:"-"`

	Command     []string `json:"command" gorm:"serializer:json"`
	Entrypoint  []string `json:"entrypoint,omitempty" gorm:"serializer:json"`
	Image       string   `json:"image,omitempty"`
	RegistryID  *uint    `json:"registry_id,omitempty"`
	RunAsUser   string   `json:"run_as_user,omitempty"`
	TimeoutSecs int      `json:"timeout_secs" gorm:"not null;default:0"`

	RunPolicy     string `json:"run_policy" gorm:"not null;default:onChange"`
	WaitForDeploy bool   `json:"wait_for_deploy" gorm:"not null;default:true"`
	HistoryLimit  int    `json:"history_limit" gorm:"not null;default:0"`
	// BackoffLimit is how many times a failed run is retried; 0 never retries.
	BackoffLimit int `json:"backoff_limit" gorm:"not null;default:0"`

	// SpecHash fingerprints the run template as last applied from the manifest;
	// a sync runs an onChange definition only when it changes.
	SpecHash string `json:"spec_hash"`
	// LastReleaseID is the release an onRelease definition last ran against.
	LastReleaseID *uint `json:"last_release_id,omitempty"`
	// LastJobID is the most recent run; LastStatus is its state (transient).
	LastJobID  *uint     `json:"last_job_id,omitempty"`
	LastStatus JobStatus `json:"last_status,omitempty" gorm:"-"`

	Metadata    Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	Annotations Metadata  `json:"annotations,omitempty" gorm:"serializer:json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
