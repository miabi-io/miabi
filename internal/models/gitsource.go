// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
)

// GitSource binds a Git path of miabi.io/v1 manifests to a workspace as a
// continuously-reconciled desired-state source — the GitOps half of the declarative model:
// Git -> (sync) -> Postgres desired -> (reconciler) -> Docker.
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

	// GitRepositoryID optionally references stored credentials (token/SSH) for a
	// private repository. nil means the repo is public.
	GitRepositoryID *uint `json:"git_repository_id,omitempty" gorm:"index"`

	SyncPolicy GitSyncPolicy `json:"sync_policy" gorm:"not null;default:manual"`
	// Prune deletes managed resources that disappear from Git (opt-in for safety).
	Prune bool `json:"prune" gorm:"not null;default:false"`
	// SelfHeal re-applies when the live state drifts from Git.
	SelfHeal bool `json:"self_heal" gorm:"not null;default:false"`
	// AllowEmpty permits an empty manifest set to prune ALL managed resources (intentional
	// teardown). Off by default, so a wrong path or a wiped repo cannot tear down a deployment.
	// A missing path is always an error. Only meaningful together with Prune.
	AllowEmpty bool `json:"allow_empty" gorm:"not null;default:false"`

	// WebhookSecret authenticates inbound provider push webhooks. Never serialized.
	WebhookSecret string `json:"-" gorm:"not null"`

	Status           GitSourceStatus `json:"status" gorm:"not null;default:unknown"`
	Message          string          `json:"message,omitempty" gorm:"type:text"`
	LastSyncedCommit string          `json:"last_synced_commit,omitempty"`
	// LastSyncedAuthor/Subject record who authored the synced commit and its
	// subject line, for display on the detail page.
	LastSyncedAuthor  string     `json:"last_synced_author,omitempty"`
	LastSyncedSubject string     `json:"last_synced_subject,omitempty"`
	LastSyncedAt      *time.Time `json:"last_synced_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
