// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// PipelineSource records who owns a definition's spec.
type PipelineSource string

const (
	// PipelineSourceManual is a spec authored in Miabi: the UI and API own it.
	PipelineSourceManual PipelineSource = "manual"
	// PipelineSourceRepo is a spec adopted from the repository's pipeline-as-code
	// file. The repo is the source of truth — the spec is re-read at each run's
	// commit, and Miabi refuses to edit it in place.
	PipelineSourceRepo PipelineSource = "repo"
)

// PipelineDefinition is a versioned, pipeline-as-code definition
// (kind: Pipeline). The raw YAML spec is stored so the runner reads the same
// document the repo carries at .miabi/pipeline.yaml.
type PipelineDefinition struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_pipeline_ws_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_pipeline_ws_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`
	// ApplicationID optionally binds the pipeline's deploy step to an app.
	ApplicationID *uint `json:"application_id,omitempty" gorm:"index"`
	// Spec is the raw kind: Pipeline YAML (on/steps). Parsed at run time.
	Spec    string `json:"spec" gorm:"type:text;not null"`
	Enabled bool   `json:"enabled" gorm:"not null;default:true"`
	// WebhookSecret authenticates inbound provider push webhooks that fire this
	// pipeline. Generated on create; never serialized.
	WebhookSecret string `json:"-" gorm:"not null;default:''"`

	// Source records who owns Spec. An empty value reads as manual, so pipelines
	// created before repository adoption existed keep behaving exactly as before.
	Source PipelineSource `json:"source,omitempty" gorm:"not null;default:manual"`
	// SourcePath is the repo-relative path Spec was read from (repo source only),
	// e.g. ".miabi/pipeline.yaml".
	SourcePath string `json:"source_path,omitempty"`
	// SourceRef is the branch or ref the spec is read from (repo source only).
	// It is the app's tracked ref at adoption time.
	SourceRef string `json:"source_ref,omitempty"`
	// SourceCommit is the commit Spec was last read at (repo source only) — what
	// the UI shows as "synced from <commit>".
	SourceCommit string `json:"source_commit,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// LastRun is a non-persisted summary of the pipeline's most recent run,
	// populated by the list endpoint for an at-a-glance health signal. nil when
	// the pipeline has never run.
	LastRun *PipelineRunSummary `json:"last_run,omitempty" gorm:"-"`
}

// IsRepoOwned reports whether the repository owns this pipeline's spec, which
// makes it read-only in Miabi and re-read on every run.
func (p *PipelineDefinition) IsRepoOwned() bool { return p.Source == PipelineSourceRepo }

// PipelineRunSummary is a lightweight view of a run for list contexts — enough
// to render a status badge and "ran 2h ago" without shipping steps or logs.
type PipelineRunSummary struct {
	ID         uint              `json:"id"`
	Number     int               `json:"number"`
	Status     PipelineRunStatus `json:"status"`
	Trigger    string            `json:"trigger,omitempty"`
	Commit     string            `json:"commit,omitempty"`
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

// PipelineRun is one execution of a PipelineDefinition: build → test → … →
// deploy, turning a commit into an image and a Release.
type PipelineRun struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index;not null"`
	PipelineID  uint `json:"pipeline_id" gorm:"index;not null"`
	// Number is the per-pipeline monotonic run counter (#1, #2, …).
	Number int               `json:"number" gorm:"not null"`
	Status PipelineRunStatus `json:"status" gorm:"not null;default:pending"`
	// Trigger records how the run started: push | manual | schedule | upstream.
	Trigger string `json:"trigger"`
	// Branch is the ref the run built, surfaced to steps as $MIABI_BRANCH. Set
	// from the pushed ref, or from the app's tracked ref for a manual run.
	Branch        string `json:"branch,omitempty"`
	Commit        string `json:"commit,omitempty"`
	CommitMessage string `json:"commit_message,omitempty" gorm:"type:text"`
	// TriggeredByKeyID / TriggeredByUserID attribute the run (mirrors Deployment).
	TriggeredByKeyID  *uint `json:"triggered_by_key_id,omitempty"`
	TriggeredByUserID *uint `json:"triggered_by_user_id,omitempty"`
	// ImageID is the artifact the run produced (nil until the build step pushes).
	ImageID *uint `json:"image_id,omitempty"`
	// RunnerID records which runner executed this run (provenance / "built on
	// runner X"); nil when it ran on the internal runner.
	RunnerID *uint  `json:"runner_id,omitempty"`
	Error    string `json:"error,omitempty" gorm:"type:text"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`

	Steps []PipelineStepRun `json:"steps,omitempty" gorm:"foreignKey:PipelineRunID"`
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
	// Run is the shell command for a container (Image) step, executed in a non-login shell inside
	// the step image — like a GitHub Actions `run:` step — so pipes, && and env expansion work.
	// Empty for `uses:` built-in steps.
	Run string `json:"run,omitempty" gorm:"type:text"`
	// Dockerfile and BuildContext configure a `uses: build` step, relative to the checked-out
	// source root; empty means "Dockerfile" and the root. Recorded per RUN because a run is the
	// audit record of what was built, not of the definition as it reads today.
	Dockerfile   string `json:"dockerfile,omitempty"`
	BuildContext string `json:"build_context,omitempty"`
	// BuildArgs are the step's Dockerfile ARG values, recorded per run as part of what produced
	// the image. Not secrets — a build arg is readable from the image history — so they are
	// stored in the clear, unlike the run's credentials.
	BuildArgs map[string]string `json:"build_args,omitempty" gorm:"serializer:json"`
	// ContinueOnError lets the run keep going (and still succeed) when this step
	// fails; the step is still recorded failed.
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
