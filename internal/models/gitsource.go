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

// GitSyncPolicy controls when a GitSource reconciles.
type GitSyncPolicy string

const (
	// GitSyncManual reconciles only on an explicit Sync (or webhook) request.
	GitSyncManual GitSyncPolicy = "manual"
	// GitSyncAuto reconciles on the polling sweep and on webhook push.
	GitSyncAuto GitSyncPolicy = "auto"
)

// GitSourceStatus is the aggregate sync state of a GitSource.
type GitSourceStatus string

const (
	GitSourceUnknown     GitSourceStatus = "unknown"
	GitSourceSynced      GitSourceStatus = "synced"
	GitSourceOutOfSync   GitSourceStatus = "out_of_sync"
	GitSourceProgressing GitSourceStatus = "progressing"
	GitSourceError       GitSourceStatus = "error"
	// GitSourceDegraded is synced, but a Job it declares failed its last run.
	GitSourceDegraded GitSourceStatus = "degraded"
)

// GitSource binds a Git path of manifests to a workspace as a continuously reconciled desired-state source.
type GitSource struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_gitsource_ws_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_gitsource_ws_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`

	RepoURL string `json:"repo_url" gorm:"not null"`
	Ref     string `json:"ref" gorm:"not null;default:main"` // branch, tag or commit
	Path    string `json:"path" gorm:"not null;default:."`   // manifest subdirectory

	// nil means the repo is public.
	GitRepositoryID *uint `json:"git_repository_id,omitempty" gorm:"index"`

	SyncPolicy GitSyncPolicy `json:"sync_policy" gorm:"not null;default:manual"`
	// Prune deletes managed resources that disappear from Git (opt-in for safety).
	Prune bool `json:"prune" gorm:"not null;default:false"`
	// SelfHeal re-applies when the live state drifts from Git.
	SelfHeal bool `json:"self_heal" gorm:"not null;default:false"`
	// Off by default so a wrong path or wiped repo cannot tear down a deployment; only meaningful with Prune.
	AllowEmpty bool `json:"allow_empty" gorm:"not null;default:false"`
	// SealedSecret and generated Secrets still pass.
	RequireSealedSecrets bool   `json:"require_sealed_secrets" gorm:"not null;default:false"`
	DeletePolicy         string `json:"delete_policy" gorm:"not null;default:orphan"`

	Metadata    Metadata `json:"metadata,omitempty" gorm:"serializer:json"`
	Annotations Metadata `json:"annotations,omitempty" gorm:"serializer:json"`

	// WebhookSecret authenticates inbound provider push webhooks. Never serialized.
	WebhookSecret string `json:"-" gorm:"not null"`

	Status  GitSourceStatus `json:"status" gorm:"not null;default:unknown"`
	Message string          `json:"message,omitempty" gorm:"type:text"`

	LastSyncedCommit  string     `json:"last_synced_commit,omitempty"`
	LastSyncedAuthor  string     `json:"last_synced_author,omitempty"`
	LastSyncedSubject string     `json:"last_synced_subject,omitempty"`
	LastSyncedAt      *time.Time `json:"last_synced_at,omitempty"`

	LastCheckedCommit string     `json:"last_checked_commit,omitempty"`
	LastCheckedAt     *time.Time `json:"last_checked_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
