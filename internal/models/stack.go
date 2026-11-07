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

// Stack's DockerName is applied as the com.docker.compose.project label so Docker tooling groups the apps too.
type Stack struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_stack_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_stack_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`
	DockerName  string `json:"docker_name" gorm:"uniqueIndex;not null"`
	ClusterID   uint   `json:"cluster_id" gorm:"index;not null;default:0"`
	// Gives the stack's apps service-name DNS isolated from other stacks.
	DockerNetwork string `json:"docker_network,omitempty"`
	Description   string `json:"description,omitempty"`
	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata    Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	Annotations Metadata  `json:"annotations,omitempty" gorm:"serializer:json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// An application belongs to at most one stack.
	Apps []Application `json:"apps,omitempty" gorm:"foreignKey:StackID"`
}

// StackEnvVar is injected into every member app at deploy; app-level vars with the same key win.
type StackEnvVar struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	StackID   uint      `json:"stack_id" gorm:"index:idx_stack_env_key,unique;not null"`
	Key       string    `json:"key" gorm:"index:idx_stack_env_key,unique;not null"`
	Value     string    `json:"value"` // plaintext for non-secret; ciphertext when IsSecret
	IsSecret  bool      `json:"is_secret" gorm:"not null;default:false"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Derived on read: an app-level variable wins, so a shared value can be inert for some members.
	OverriddenBy []string `json:"overridden_by,omitempty" gorm:"-"`
}
