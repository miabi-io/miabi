// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// RunnerStatus is the reachability state of a runner, mirroring ServerStatus.
type RunnerStatus string

const (
	// RunnerStatusOnline: the runner has a live tunnel and is leasing jobs.
	RunnerStatusOnline RunnerStatus = "online"
	// RunnerStatusOffline: no live tunnel (never connected, or heartbeat lapsed).
	RunnerStatusOffline RunnerStatus = "offline"
	// RunnerStatusDraining: connected but finishing its in-flight jobs before it
	// disconnects (no new leases). Distinct from Cordoned, which is an operator
	// hold applied while the runner may still be online.
	RunnerStatusDraining RunnerStatus = "draining"
)

// RunnerScope is who may schedule jobs onto a runner.
type RunnerScope string

const (
	// ScopeWorkspace: a runner registered and owned by a single workspace; only
	// that workspace's jobs run on it (WorkspaceID is set).
	ScopeWorkspace RunnerScope = "workspace"
	// ScopeShared: a platform-shared runner (WorkspaceID is nil) usable by any
	// workspace whose plan grants the platform-runners capability. Managed by
	// admins, mirroring a global TemplateSource/OAuthProvider.
	ScopeShared RunnerScope = "shared"
)

// ValidRunnerScope reports whether s is a known runner scope.
func ValidRunnerScope(s RunnerScope) bool {
	switch s {
	case ScopeWorkspace, ScopeShared:
		return true
	}
	return false
}

// Runner is a machine dedicated to build and pipeline execution: it leases jobs, runs each
// step in an isolated container, pushes the image, and reports status — app nodes only pull
// the digest. Either workspace-owned (Scope=workspace) or platform-shared (Scope=shared).
type Runner struct {
	UIDModel
	ID uint `json:"id" gorm:"primaryKey"`
	// Name is a URL-safe slug handle, unique within its owner (per workspace for
	// owned runners; among shared runners for platform ones).
	Name string `json:"name" gorm:"index:idx_runner_ws_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI (defaults to Name).
	DisplayName string `json:"display_name"`
	// WorkspaceID is nil for platform-shared runners, set for workspace-owned
	// ones. Part of the (workspace_id, name) uniqueness index.
	WorkspaceID *uint       `json:"workspace_id,omitempty" gorm:"index:idx_runner_ws_name,unique"`
	Scope       RunnerScope `json:"scope" gorm:"not null;default:workspace"`
	// Labels route jobs to eligible runners (e.g. "arch=amd64", "buildkit",
	// "gpu"). A job runs only where its required labels are a subset of these.
	Labels []string `json:"labels,omitempty" gorm:"serializer:json"`
	// Concurrency is how many jobs the runner may lease at once (declared by the
	// operator; the scheduler tracks live leases against it).
	Concurrency int `json:"concurrency" gorm:"not null;default:1"`

	// Self-reported platform facts (filled on connect).
	OS      string `json:"os,omitempty"`
	Arch    string `json:"arch,omitempty"`
	Version string `json:"version,omitempty"`
	// RemoteIP is the source address of the runner's most recent tunnel
	// connection (last known; persists across disconnects for the detail view).
	RemoteIP string `json:"remote_ip,omitempty"`

	Status RunnerStatus `json:"status" gorm:"not null;default:offline"`
	// Cordoned holds the runner out of scheduling without disconnecting it
	// (operator pause), mirroring Server.Cordoned.
	Cordoned bool `json:"cordoned" gorm:"not null;default:false"`
	// Enabled is the admin/owner on-off switch; a disabled runner never receives
	// jobs even if connected. Defaults true.
	Enabled bool `json:"enabled" gorm:"not null;default:true"`
	// TokenHash is the SHA-256 of the one-time registration token; the plaintext
	// is shown once at creation and never stored (mirrors Server.TokenHash).
	TokenHash string `json:"-" gorm:"index"`
	// Ephemeral marks a per-job, autoscaled runner (spun up for one job and torn
	// down after). Standing runners are non-ephemeral. Reserved for the later
	// autoscaling phase; excluded from the owned-runner quota when set.
	Ephemeral bool `json:"ephemeral" gorm:"not null;default:false"`
	// Connected reflects a live tunnel (transient; set by the connection manager).
	Connected bool `json:"connected" gorm:"-"`

	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	// ConnectedSince is when the current unbroken connection began — reset on reconnect, cleared
	// on disconnect. LastSeenAt cannot answer "how long has this runner been up?" because the
	// heartbeat refreshes it every 30s, which the offline alert needs to survive flapping.
	ConnectedSince *time.Time `json:"connected_since,omitempty"`
	CreatedByID    *uint      `json:"created_by_id,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RunnerLeaseStatus is the state of a runner's claim on a pipeline run.
type RunnerLeaseStatus string

const (
	// LeaseActive: the runner holds the job and is executing it. Exactly one
	// active lease may exist per run (the at-most-once claim).
	LeaseActive RunnerLeaseStatus = "active"
	// LeaseDone: the run reached a terminal state and the lease was released.
	LeaseDone RunnerLeaseStatus = "done"
	// LeaseExpired: the lease passed its deadline without completing (a dead
	// runner); the sweeper requeues the run onto another runner.
	LeaseExpired RunnerLeaseStatus = "expired"
)

// LeaseKind namespaces a lease's RunID so pipeline runs and deploy builds — two
// different id spaces — never collide in the shared runner_leases table.
type LeaseKind string

const (
	// LeaseKindPipeline: RunID is a PipelineRun id.
	LeaseKindPipeline LeaseKind = "pipeline"
	// LeaseKindBuild: RunID is a Deployment id (a git-app build dispatched to a
	// runner, outside any pipeline).
	LeaseKindBuild LeaseKind = "build"
)

// RunnerLease is a runner's at-most-once claim on a job (per Kind) and optionally a step,
// created on lease and released when the job goes terminal. An active lease past ExpiresAt is
// a dead runner: the sweeper expires it and the job requeues.
type RunnerLease struct {
	ID       uint `json:"id" gorm:"primaryKey"`
	RunnerID uint `json:"runner_id" gorm:"index;not null"`
	// Kind namespaces RunID (pipeline run vs deploy build); see LeaseKind.
	Kind LeaseKind `json:"kind" gorm:"not null;default:pipeline"`
	// RunID is the job this lease claims (a PipelineRun or a Deployment, per Kind).
	// A partial unique index on (kind, run_id) for active rows enforces the
	// at-most-one active claim per job at the DB level.
	RunID  uint  `json:"run_id" gorm:"index;not null"`
	StepID *uint `json:"step_id,omitempty"`

	Status   RunnerLeaseStatus `json:"status" gorm:"not null;default:active;index"`
	LeasedAt time.Time         `json:"leased_at"`
	// ExpiresAt is the lease deadline; a live runner renews it, a dead one lets it
	// lapse (→ requeue). Mirrors the job deadline.
	ExpiresAt time.Time `json:"expires_at" gorm:"index"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
