// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"time"
)

// Network is a managed Docker network owned by a workspace. The user-facing
// Name is unique per workspace; DockerName is the platform-managed, globally
// unique Docker network name (workspace + short id).
type Network struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_network_workspace_name,unique;not null"`
	// Name is the unique slug handle (lowercase [a-z0-9-]) scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_network_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`
	DockerName  string `json:"docker_name" gorm:"uniqueIndex;not null"`
	Driver      string `json:"driver" gorm:"not null;default:bridge"`
	Internal    bool   `json:"internal" gorm:"not null;default:false"`
	IsDefault   bool   `json:"is_default" gorm:"not null;default:false"`
	// Imported marks a network that references a pre-existing external Docker
	// network by its own name (DockerName = the existing name; nothing recreated).
	Imported  bool      `json:"imported" gorm:"not null;default:false"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SwarmScoped reports whether the network is a swarm overlay. A worker cannot see one until a
// container there attaches, so ensuring it by name on a worker would create a same-named bridge.
func (n *Network) SwarmScoped() bool { return n.Driver == "overlay" }
