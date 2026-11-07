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

// SecurityPolicy: the most specific scope wins but may only tighten the platform rule unless it sets AllowExceptions.
type SecurityPolicy struct {
	ID        uint   `json:"id" gorm:"primaryKey"`
	Kind      string `json:"kind" gorm:"not null;uniqueIndex:idx_secpolicy_scope"`       // ports
	ScopeType string `json:"scope_type" gorm:"not null;uniqueIndex:idx_secpolicy_scope"` // platform | plan | organization | workspace
	ScopeID   uint   `json:"scope_id" gorm:"not null;default:0;uniqueIndex:idx_secpolicy_scope"`
	Mode      string `json:"mode" gorm:"not null;default:off"` // off | audit | enforce
	// AllowExceptions lets a narrower scope loosen this rule. Meaningful on the platform rule only.
	AllowExceptions bool   `json:"allow_exceptions" gorm:"not null;default:false"`
	Spec            string `json:"spec" gorm:"type:text;not null;default:'{}'"`
	Version         int    `json:"version" gorm:"not null;default:1"`
	UpdatedBy       *uint  `json:"updated_by,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SecurityEvent records denials, audit-mode would-denies and policy changes; allows are not recorded.
type SecurityEvent struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	CreatedAt   time.Time `json:"created_at" gorm:"index"`
	Kind        string    `json:"kind" gorm:"index;not null"`     // ports | policy
	Action      string    `json:"action" gorm:"not null"`         // request | import | publish | change
	Decision    string    `json:"decision" gorm:"index;not null"` // deny | would_deny | change
	UserID      *uint     `json:"user_id,omitempty"`
	WorkspaceID *uint     `json:"workspace_id,omitempty" gorm:"index"`
	Resource    string    `json:"resource,omitempty"`
	PolicyID    *uint     `json:"policy_id,omitempty"`
	Scope       string    `json:"scope,omitempty"` // e.g. "platform", "workspace:12"
	Reason      string    `json:"reason,omitempty" gorm:"type:text"`
}
