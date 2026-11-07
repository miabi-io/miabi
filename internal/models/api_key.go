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

// An empty scope set means read-only; "*" grants everything.
const (
	ScopeRead          = "read"           // read-only access to resources
	ScopeWrite         = "write"          // create/update/delete resources
	ScopeDeploy        = "deploy"         // trigger deployments and lifecycle actions
	ScopeAdmin         = "admin"          // administrative operations
	ScopeAll           = "*"              // all scopes
	ScopeRegistryRead  = "registry_read"  // pull from the container registry
	ScopeRegistryWrite = "registry_write" // push to the container registry
	// "*" does not imply billing: an all-powerful key must not be able to suspend every customer.
	ScopeBilling = "billing"
	// Carries every secret value: minted by its own action, never chosen, and implied by nothing.
	ScopeMigration = "migration"
)

// API key purposes: what a key may be presented to.
const (
	KeyPurposeGeneral   = "general"
	KeyPurposeRegistry  = "registry"
	KeyPurposeBilling   = "billing"
	KeyPurposeMigration = "migration"
)

// ValidScopes is the set of scopes accepted on API-key creation.
var ValidScopes = map[string]bool{
	ScopeRead:          true,
	ScopeWrite:         true,
	ScopeDeploy:        true,
	ScopeAdmin:         true,
	ScopeAll:           true,
	ScopeRegistryRead:  true,
	ScopeRegistryWrite: true,
}

// generalScopes grant access to the general API; a key with none of these is registry-only.
var generalScopes = map[string]bool{
	ScopeRead: true, ScopeWrite: true, ScopeDeploy: true, ScopeAdmin: true, ScopeAll: true,
}

var scopeImplies = map[string]map[string]bool{
	ScopeAll:    {ScopeAll: true, ScopeAdmin: true, ScopeWrite: true, ScopeDeploy: true, ScopeRead: true},
	ScopeAdmin:  {ScopeAdmin: true, ScopeWrite: true, ScopeDeploy: true, ScopeRead: true},
	ScopeWrite:  {ScopeWrite: true, ScopeRead: true},
	ScopeDeploy: {ScopeDeploy: true, ScopeRead: true},
	ScopeRead:   {ScopeRead: true},
	// Only a billing key satisfies the billing scope; nothing else implies it.
	ScopeBilling:   {ScopeBilling: true},
	ScopeMigration: {ScopeMigration: true},
}

// An empty grant means read-only; an empty requirement is satisfied by anything.
func ScopeSatisfies(granted []string, required string) bool {
	if required == "" {
		return true
	}
	if len(granted) == 0 {
		granted = []string{ScopeRead}
	}
	for _, g := range granted {
		if scopeImplies[g][required] {
			return true
		}
	}
	return false
}

// A key with neither a general nor the billing scope is registry-only; empty scopes count as read.
func (k *APIKey) Purpose() string {
	if len(k.Scopes) == 0 {
		return KeyPurposeGeneral
	}
	for _, s := range k.Scopes {
		switch s {
		case ScopeBilling:
			return KeyPurposeBilling
		case ScopeMigration:
			return KeyPurposeMigration
		}
	}
	for _, s := range k.Scopes {
		if generalScopes[s] {
			return KeyPurposeGeneral
		}
	}
	return KeyPurposeRegistry
}

type APIKey struct {
	ID            uint       `json:"id" gorm:"primaryKey"`
	UserID        uint       `json:"user_id" gorm:"index;not null"`
	WorkspaceID   *uint      `json:"workspace_id,omitempty" gorm:"index"`
	Name          string     `json:"name" gorm:"not null"`
	KeyHash       string     `json:"-" gorm:"uniqueIndex;not null"`
	KeyPrefix     string     `json:"key_prefix" gorm:"index;not null"`
	Scopes        []string   `json:"scopes" gorm:"serializer:json"`
	AllowedIPs    []string   `json:"allowed_ips" gorm:"serializer:json"`
	ApplicationID *uint      `json:"application_id,omitempty" gorm:"index"`
	Ephemeral     bool       `json:"ephemeral" gorm:"not null;default:false;index"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	Revoked       bool       `json:"revoked" gorm:"default:false;not null"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (k *APIKey) BoundToApp(appID uint) bool {
	return k.ApplicationID != nil && *k.ApplicationID == appID
}

// An empty scope set defaults to read-only; "*" grants everything.
func (k *APIKey) HasScope(s string) bool {
	if len(k.Scopes) == 0 {
		return s == ScopeRead
	}
	for _, sc := range k.Scopes {
		if sc == ScopeAll || sc == s {
			return true
		}
	}
	return false
}

// IsExpired reports whether the key has passed its expiry.
func (k *APIKey) IsExpired() bool {
	return k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt)
}

// IsValid reports whether the key is currently usable (not revoked, not expired).
func (k *APIKey) IsValid() bool {
	return !k.Revoked && !k.IsExpired()
}
