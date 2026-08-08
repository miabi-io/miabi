// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"time"
)

// Stack groups related applications in a workspace so they can be managed together. Name is
// unique per workspace; DockerName is the platform-managed Compose project name applied as
// the com.docker.compose.project label, so Docker tooling groups them too.
type Stack struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_stack_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_stack_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`
	DockerName  string `json:"docker_name" gorm:"uniqueIndex;not null"`
	// DockerNetwork is the platform-managed Docker network shared by the stack's
	// apps, giving them service-name DNS discovery isolated from other stacks.
	DockerNetwork string `json:"docker_network,omitempty"`
	Description   string `json:"description,omitempty"`
	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata Metadata `json:"metadata,omitempty" gorm:"serializer:json"`
	// Annotations holds free-form, non-identifying descriptive metadata (the
	// manifest's metadata.annotations); no reserved keys. Persisted as JSON.
	Annotations Metadata  `json:"annotations,omitempty" gorm:"serializer:json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Apps are the applications assigned to this stack. Populated on the stack
	// detail view; an application belongs to at most one stack.
	Apps []Application `json:"apps,omitempty" gorm:"foreignKey:StackID"`
}

// StackEnvVar is a shared environment variable injected into every member
// application's containers at deploy time. App-level vars with the same key
// take precedence. Secret values are encrypted at rest.
type StackEnvVar struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	StackID   uint      `json:"stack_id" gorm:"index:idx_stack_env_key,unique;not null"`
	Key       string    `json:"key" gorm:"index:idx_stack_env_key,unique;not null"`
	Value     string    `json:"value"` // plaintext for non-secret; ciphertext when IsSecret
	IsSecret  bool      `json:"is_secret" gorm:"not null;default:false"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
