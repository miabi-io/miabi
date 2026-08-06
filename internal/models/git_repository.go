// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// GitRepository is a stored Git credential used to clone private repositories
// at build time. The Secret (token or SSH private key) is encrypted at rest and
// never serialized.
type GitRepository struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_gitrepo_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_gitrepo_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string      `json:"display_name"`
	URL         string      `json:"url" gorm:"not null"`
	AuthType    GitAuthType `json:"auth_type" gorm:"not null;default:token"`
	// Username for HTTPS basic auth. Empty defaults to a provider-friendly value
	// ("x-access-token") at clone time.
	Username string `json:"username"`
	// Secret holds the access token (token auth) or SSH private key (ssh auth),
	// encrypted at rest. Empty when the credential points at the vault instead
	// (see SecretRef).
	Secret string `json:"-" gorm:"not null"`
	// SecretRef names a workspace Secret holding the token or key, instead of
	// storing a copy here (the `${{ secrets.NAME }}` form a client sends in the
	// secret field). The value is read from the vault at every clone, so rotating
	// that secret rotates this credential with no edit here. Mutually exclusive
	// with Secret. Not sensitive — it is a name, so it is returned by the API.
	SecretRef string `json:"secret_ref,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// HasSecret is a transient flag for responses (never persisted): true when the
	// credential can authenticate, whether from its own stored secret or a vault
	// reference.
	HasSecret bool `json:"has_secret" gorm:"-"`
}
