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

// RunnerStatus is the reachability state of a runner, mirroring ServerStatus.
type RunnerStatus string

const (
	// RunnerStatusOnline: the runner has a live tunnel and is leasing jobs.
	RunnerStatusOnline RunnerStatus = "online"
	// RunnerStatusOffline: no live tunnel (never connected, or heartbeat lapsed).
	RunnerStatusOffline RunnerStatus = "offline"
	// Distinct from Cordoned, an operator hold applied while the runner may still be online.
	RunnerStatusDraining RunnerStatus = "draining"
)

// RunnerScope is who may schedule jobs onto a runner.
type RunnerScope string

const (
	ScopeWorkspace RunnerScope = "workspace"
	// Usable by any workspace whose plan grants the platform-runners capability.
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

// Runner leases jobs, runs steps in isolated containers and pushes the image; app nodes only pull the digest.
type Runner struct {
	UIDModel
	ID   uint   `json:"id" gorm:"primaryKey"`
	Name string `json:"name" gorm:"index:idx_runner_ws_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI (defaults to Name).
	DisplayName string      `json:"display_name"`
	WorkspaceID *uint       `json:"workspace_id,omitempty" gorm:"index:idx_runner_ws_name,unique"`
	Scope       RunnerScope `json:"scope" gorm:"not null;default:workspace"`
	// A job runs only where its required labels are a subset of these.
	Labels      []string `json:"labels,omitempty" gorm:"serializer:json"`
	Concurrency int      `json:"concurrency" gorm:"not null;default:1"`

	// Self-reported platform facts (filled on connect).
	OS      string `json:"os,omitempty"`
	Arch    string `json:"arch,omitempty"`
	Version string `json:"version,omitempty"`
	// LatestVersion is set, transiently, when a newer runner release exists.
	LatestVersion string `json:"latest_version,omitempty" gorm:"-"`
	// Jobs needing a feature go only to runners listing it, since an older runner would silently drop the request.
	Features []string `json:"features,omitempty" gorm:"serializer:json"`
	RemoteIP string   `json:"remote_ip,omitempty"`

	Status   RunnerStatus `json:"status" gorm:"not null;default:offline"`
	Cordoned bool         `json:"cordoned" gorm:"not null;default:false"`
	Enabled  bool         `json:"enabled" gorm:"not null;default:true"`
	// The plaintext is shown once at creation and never stored.
	TokenHash string `json:"-" gorm:"index"`
	// Reserved for later autoscaling; excluded from the owned-runner quota when set.
	Ephemeral bool `json:"ephemeral" gorm:"not null;default:false"`
	// Connected reflects a live tunnel (transient; set by the connection manager).
	Connected bool `json:"connected" gorm:"-"`

	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	// LastSeenAt cannot answer uptime since the heartbeat refreshes it every 30s; the offline alert needs this to survive flapping.
	ConnectedSince *time.Time `json:"connected_since,omitempty"`
	CreatedByID    *uint      `json:"created_by_id,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RunnerLeaseStatus is the state of a runner's claim on a pipeline run.
type RunnerLeaseStatus string

const (
	// Exactly one active lease may exist per run (at-most-once claim).
	LeaseActive RunnerLeaseStatus = "active"
	// LeaseDone: the run reached a terminal state and the lease was released.
	LeaseDone RunnerLeaseStatus = "done"
	// The sweeper requeues the run onto another runner.
	LeaseExpired RunnerLeaseStatus = "expired"
)

// Pipeline runs and deploy builds are separate id spaces sharing runner_leases.
type LeaseKind string

const (
	// LeaseKindPipeline: RunID is a PipelineRun id.
	LeaseKindPipeline LeaseKind = "pipeline"
	LeaseKindBuild    LeaseKind = "build"
)

// RunnerLease is an at-most-once job claim; an active lease past ExpiresAt is a dead runner and the job requeues.
type RunnerLease struct {
	ID       uint `json:"id" gorm:"primaryKey"`
	RunnerID uint `json:"runner_id" gorm:"index;not null"`
	// Kind namespaces RunID (pipeline run vs deploy build); see LeaseKind.
	Kind LeaseKind `json:"kind" gorm:"not null;default:pipeline"`
	// A partial unique index on (kind, run_id) for active rows enforces one active claim per job.
	RunID  uint  `json:"run_id" gorm:"index;not null"`
	StepID *uint `json:"step_id,omitempty"`

	Status    RunnerLeaseStatus `json:"status" gorm:"not null;default:active;index"`
	LeasedAt  time.Time         `json:"leased_at"`
	ExpiresAt time.Time         `json:"expires_at" gorm:"index"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RunnerFacts are what a runner reports about itself when its tunnel connects.
type RunnerFacts struct {
	OS, Arch, Version, RemoteIP string
	Features                    []string
}
