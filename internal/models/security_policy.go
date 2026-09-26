// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// SecurityPolicy is one Security Center rule: a kind of control, the scope it applies to, whether
// it is enforced, and a versioned JSON spec the kind's evaluator reads. The most specific scope
// wins, but may only tighten the platform rule unless that rule sets AllowExceptions.
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

// SecurityEvent is one decision a policy made: a denial, an audit-mode "would deny", or a change to
// a policy itself. Allows are not recorded.
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
