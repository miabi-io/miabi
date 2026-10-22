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

// Organization is the tenant boundary: it owns workspaces, caps how many, and may own the clusters
// they deploy to. It is also the identity realm SSO, SAML, enforced login and SCIM attach to. Miabi
// seeds one default organization; every nullable organization_id resolves to it.
//
// It is deliberately NOT a data-isolation boundary — every resource is scoped by workspace_id in the
// repository layer, and that stays the isolation mechanism.
type Organization struct {
	UIDModel
	ID                             uint      `json:"id" gorm:"primaryKey"`
	Name                           string    `json:"name" gorm:"uniqueIndex;not null"`
	DisplayName                    string    `json:"display_name"`
	IsDefault                      bool      `json:"is_default" gorm:"default:false;not null"`
	OwnerUserID                    uint      `json:"owner_user_id" gorm:"index"`
	MaxWorkspaces                  int       `json:"max_workspaces" gorm:"not null;default:-1"`
	MaxWorkspacesPerUser           *int      `json:"max_workspaces_per_user,omitempty"`
	MaxWorkspaceMembershipsPerUser *int      `json:"max_workspace_memberships_per_user,omitempty"`
	MaxWorkspacesPerOwner          *int      `json:"max_workspaces_per_owner,omitempty"`
	WorkspaceCreation              *string   `json:"workspace_creation,omitempty"`
	DefaultClusterID               *uint     `json:"default_cluster_id,omitempty" gorm:"index"`
	EnforceSSO                     bool      `json:"enforce_sso" gorm:"default:false;not null"`
	CreatedAt                      time.Time `json:"created_at"`
	UpdatedAt                      time.Time `json:"updated_at"`
	WorkspaceCount                 int64     `json:"workspace_count" gorm:"-"`
}

// DefaultOrganizationName is the handle of the seeded default organization, and
// DefaultOrganizationLabel the label it is seeded with.
const (
	DefaultOrganizationName  = "default"
	DefaultOrganizationLabel = "Miabi Default"
)

// WorkspacesUnlimited reports whether the org has no workspace cap.
func (o *Organization) WorkspacesUnlimited() bool { return o != nil && o.MaxWorkspaces < 0 }
