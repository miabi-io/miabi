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

// Network's DockerName is the platform-managed, globally unique name (workspace + short id).
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
	// DockerName is the existing name; nothing is recreated.
	Imported  bool      `json:"imported" gorm:"not null;default:false"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SwarmScoped matters because ensuring an unseen overlay by name on a worker would create a same-named bridge.
func (n *Network) SwarmScoped() bool { return n.Driver == "overlay" }
