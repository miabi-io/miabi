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

package models

import "time"

// PipelineSource records who owns a definition's spec.
type PipelineSource string

const (
	// PipelineSourceManual is a spec authored in Miabi: the UI and API own it.
	PipelineSourceManual PipelineSource = "manual"
	// PipelineSourceRepo specs are re-read at each run's commit and cannot be edited in Miabi.
	PipelineSourceRepo PipelineSource = "repo"
)

type PipelineCheckout string

const (
	CheckoutNone PipelineCheckout = "none"
	// CheckoutApplication clones the bound application's repository.
	CheckoutApplication PipelineCheckout = "application"
	// CheckoutRepository clones a registered Git repository directly.
	CheckoutRepository PipelineCheckout = "repository"
)

type PipelineDefinition struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_pipeline_ws_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_pipeline_ws_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName     string `json:"display_name"`
	ApplicationID   *uint  `json:"application_id,omitempty" gorm:"index"`
	GitRepositoryID *uint  `json:"git_repository_id,omitempty" gorm:"index"`
	// GitRepository is the bound repository's name, filled on read.
	GitRepository string `json:"git_repository,omitempty" gorm:"-"`
	// ApplicationName is the bound application's name, filled on read.
	ApplicationName string `json:"application_name,omitempty" gorm:"-"`
	Branch          string `json:"branch,omitempty"`
	// Spec is the raw kind: Pipeline YAML (on/steps). Parsed at run time.
	Spec          string `json:"spec" gorm:"type:text;not null"`
	Enabled       bool   `json:"enabled" gorm:"not null;default:true"`
	WebhookSecret string `json:"-" gorm:"not null;default:''"`

	// Empty reads as manual so pipelines created before repo adoption behave as before.
	Source     PipelineSource `json:"source,omitempty" gorm:"not null;default:manual"`
	SourcePath string         `json:"source_path,omitempty"`
	// The app's tracked ref at adoption time.
	SourceRef    string `json:"source_ref,omitempty"`
	SourceCommit string `json:"source_commit,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	LastRun *PipelineRunSummary `json:"last_run,omitempty" gorm:"-"`
}

func (p *PipelineDefinition) IsRepoOwned() bool { return p.Source == PipelineSourceRepo }

// The application binding wins if both are set, though the service refuses to store that combination.
func (p *PipelineDefinition) Checkout() PipelineCheckout {
	switch {
	case p.ApplicationID != nil:
		return CheckoutApplication
	case p.GitRepositoryID != nil:
		return CheckoutRepository
	default:
		return CheckoutNone
	}
}

type PipelineRunSummary struct {
	ID         uint              `json:"id"`
	Number     int               `json:"number"`
	Status     PipelineRunStatus `json:"status"`
	Trigger    string            `json:"trigger,omitempty"`
	Commit     string            `json:"commit,omitempty"`
	ImageTag   string            `json:"image_tag,omitempty"`
	StartedAt  *time.Time        `json:"started_at,omitempty"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

// Summary projects a run onto its list-view summary.
func (r PipelineRun) Summary() *PipelineRunSummary {
	return &PipelineRunSummary{
		ID:         r.ID,
		Number:     r.Number,
		Status:     r.Status,
		Trigger:    r.Trigger,
		Commit:     r.Commit,
		ImageTag:   r.ImageTag,
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
		CreatedAt:  r.CreatedAt,
	}
}

// PipelineRunStatus is the lifecycle state of a run or one of its steps.
type PipelineRunStatus string

const (
	PipelineRunPending   PipelineRunStatus = "pending"
	PipelineRunRunning   PipelineRunStatus = "running"
	PipelineRunSucceeded PipelineRunStatus = "succeeded"
	PipelineRunFailed    PipelineRunStatus = "failed"
	PipelineRunCanceled  PipelineRunStatus = "canceled"
)

// IsTerminal reports whether the status is final.
func (s PipelineRunStatus) IsTerminal() bool {
	return s == PipelineRunSucceeded || s == PipelineRunFailed || s == PipelineRunCanceled
}

type PipelineRun struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index;not null"`
	PipelineID  uint `json:"pipeline_id" gorm:"index;not null"`
	// Number is the per-pipeline monotonic run counter (#1, #2, …).
	Number int               `json:"number" gorm:"not null"`
	Status PipelineRunStatus `json:"status" gorm:"not null;default:pending"`
	// Trigger records how the run started: push | manual | schedule | upstream.
	Trigger string `json:"trigger"`
	// Set from the pushed ref, or the app's tracked ref for a manual run.
	Branch        string `json:"branch,omitempty"`
	Commit        string `json:"commit,omitempty"`
	CommitMessage string `json:"commit_message,omitempty" gorm:"type:text"`
	// TriggeredByKeyID / TriggeredByUserID attribute the run (mirrors Deployment).
	TriggeredByKeyID  *uint `json:"triggered_by_key_id,omitempty"`
	TriggeredByUserID *uint `json:"triggered_by_user_id,omitempty"`
	// ImageID is the artifact the run produced (nil until the build step pushes).
	ImageID *uint `json:"image_id,omitempty"`
	// Both empty for a run that builds nothing or has no version policy.
	Version  string `json:"version,omitempty"`
	ImageTag string `json:"image_tag,omitempty"`
	// nil when it ran on the internal runner.
	RunnerID *uint  `json:"runner_id,omitempty"`
	Error    string `json:"error,omitempty" gorm:"type:text"`
	// Stored unresolved, like the steps' own.
	Env map[string]string `json:"env,omitempty" gorm:"serializer:json"`
	// A per-run override of the spec's cache setting; the next run caches again unless it asks too.
	NoCache bool `json:"no_cache,omitempty" gorm:"not null;default:false"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`

	Steps []PipelineStepRun `json:"steps,omitempty" gorm:"foreignKey:PipelineRunID"`

	// Deployment is the deploy this run started, filled on the run detail read.
	Deployment *RunDeployment `json:"deployment,omitempty" gorm:"-"`
}

// RunDeployment points a run at the deployment its deploy step created.
type RunDeployment struct {
	ID              uint             `json:"id"`
	Number          int              `json:"number"`
	Status          DeploymentStatus `json:"status"`
	ApplicationID   uint             `json:"application_id"`
	ApplicationName string           `json:"application_name,omitempty"`
}

// PipelineStepRun is one step within a run, executed in an isolated container.
type PipelineStepRun struct {
	ID            uint              `json:"id" gorm:"primaryKey"`
	PipelineRunID uint              `json:"pipeline_run_id" gorm:"index;not null"`
	Ordinal       int               `json:"ordinal" gorm:"not null"`
	Name          string            `json:"name" gorm:"not null"`
	Status        PipelineRunStatus `json:"status" gorm:"not null;default:pending"`
	Image         string            `json:"image,omitempty"`
	Uses          string            `json:"uses,omitempty"` // built-in step kind (e.g. "deploy")
	// Runs in a non-login shell inside the step image so pipes, && and env expansion work.
	Run string `json:"run,omitempty" gorm:"type:text"`
	// Recorded per run because a run is the audit record of what was built, not of today's definition.
	Dockerfile   string `json:"dockerfile,omitempty"`
	BuildContext string `json:"build_context,omitempty"`
	// Stored in the clear: build args are readable from the image history, so they are not secrets.
	BuildArgs map[string]string `json:"build_args,omitempty" gorm:"serializer:json"`
	// Resolved value (spec cache: false OR the run override), so the runner reads one field.
	NoCache bool `json:"no_cache,omitempty" gorm:"not null;default:false"`
	// Platforms are the platforms this step's image was built for; empty means the runner's own.
	Platforms []string `json:"platforms,omitempty" gorm:"serializer:json"`
	// Stored as written, references included: never a resolved secret value.
	Env map[string]string `json:"env,omitempty" gorm:"serializer:json"`
	// The step is still recorded failed.
	ContinueOnError bool   `json:"continue_on_error,omitempty"`
	ExitCode        int    `json:"exit_code"`
	Logs            string `json:"logs,omitempty" gorm:"type:text"`
	LogRef          string `json:"log_ref,omitempty"`
	LogBytes        int64  `json:"log_bytes,omitempty"`
	LogLines        int    `json:"log_lines,omitempty"`
	LogTruncated    bool   `json:"log_truncated,omitempty"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}
