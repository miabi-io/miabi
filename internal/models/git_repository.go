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

import (
	"time"
)

// GitAuthType is how an application authenticates to a Git repository.
type GitAuthType string

const (
	// GitAuthPublic is an anonymous (public) repository — no credentials.
	GitAuthPublic GitAuthType = "public"
	// GitAuthToken uses HTTPS basic auth (username + personal access token).
	GitAuthToken GitAuthType = "token"
	// GitAuthSSH uses an SSH private key against an ssh:// or git@ URL.
	GitAuthSSH GitAuthType = "ssh"
)

// GitRepository is a clone credential; its Secret is encrypted at rest and never serialized.
type GitRepository struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_gitrepo_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_gitrepo_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string      `json:"display_name"`
	URL         string      `json:"url" gorm:"not null"`
	AuthType    GitAuthType `json:"auth_type" gorm:"not null;default:token"`
	// Empty defaults to "x-access-token" at clone time.
	Username string `json:"username"`
	// Encrypted at rest; empty when SecretRef points at the vault instead.
	Secret string `json:"-" gorm:"not null"`
	// Read from the vault at every clone so rotating the secret rotates this credential; mutually exclusive with Secret.
	SecretRef string `json:"secret_ref,omitempty"`

	// Metadata holds labels; "miabi.io/" keys are platform-managed (managed-by, gitops-source).
	Metadata    Metadata `json:"metadata,omitempty" gorm:"serializer:json"`
	Annotations Metadata `json:"annotations,omitempty" gorm:"serializer:json"`

	// Persisted so the list need not probe every remote per page load; a point-in-time observation, shown with its timestamp.
	ConnectionStatus GitConnectionStatus `json:"connection_status,omitempty" gorm:"not null;default:unknown"`
	// Surfaced verbatim so auth failures and missing repos stay distinguishable; they need different fixes.
	ConnectionError     string     `json:"connection_error,omitempty"`
	ConnectionCheckedAt *time.Time `json:"connection_checked_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	HasSecret bool `json:"has_secret" gorm:"-"`
}

// GitConnectionStatus is the result of the last reachability check.
type GitConnectionStatus string

const (
	// Also set when the URL or credential changed since the last check.
	GitConnectionUnknown GitConnectionStatus = "unknown"
	// GitConnectionOK means the remote answered and the credential authenticated.
	GitConnectionOK     GitConnectionStatus = "ok"
	GitConnectionFailed GitConnectionStatus = "failed"
)
