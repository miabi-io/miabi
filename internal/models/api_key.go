// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// API key scopes gate what an API-key caller may do. An empty scope set means
// read-only (least privilege); "*" grants everything.
const (
	ScopeRead   = "read"   // read-only access to resources
	ScopeWrite  = "write"  // create/update/delete resources
	ScopeDeploy = "deploy" // trigger deployments and lifecycle actions
	ScopeAdmin  = "admin"  // administrative operations
	ScopeAll    = "*"      // all scopes
	// Registry-only scopes: a key carrying ONLY these may pull/push images but is
	// rejected by the rest of the API (least-privilege CI/registry credentials).
	ScopeRegistryRead  = "registry_read"  // pull from the container registry
	ScopeRegistryWrite = "registry_write" // push to the container registry
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

// generalScopes are the scopes that grant access to the general API (everything
// outside the container registry). A key with none of these is registry-only.
var generalScopes = map[string]bool{
	ScopeRead: true, ScopeWrite: true, ScopeDeploy: true, ScopeAdmin: true, ScopeAll: true,
}

// IsRegistryOnly reports whether the key's scopes grant only registry access
// (so it must be refused by the general API). An empty scope set defaults to
// read, which is a general scope, so it is never registry-only.
func (k *APIKey) IsRegistryOnly() bool {
	if len(k.Scopes) == 0 {
		return false
	}
	for _, s := range k.Scopes {
		if generalScopes[s] {
			return false
		}
	}
	return true
}

// APIKey is a long-lived bearer credential for programmatic access, scoped to a
// user and (optionally) a workspace.
type APIKey struct {
	ID          uint     `json:"id" gorm:"primaryKey"`
	UserID      uint     `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint    `json:"workspace_id,omitempty" gorm:"index"`
	Name        string   `json:"name" gorm:"not null"`
	KeyHash     string   `json:"-" gorm:"uniqueIndex;not null"`
	KeyPrefix   string   `json:"key_prefix" gorm:"index;not null"`
	Scopes      []string `json:"scopes" gorm:"serializer:json"`
	AllowedIPs  []string `json:"allowed_ips" gorm:"serializer:json"`
	// ApplicationID narrows an ephemeral job key to a single application: it may
	// act on (deploy) only that app, nothing else in the workspace. nil = the
	// key is not app-bound (ordinary user keys).
	ApplicationID *uint `json:"application_id,omitempty" gorm:"index"`
	// Ephemeral marks a machine-minted, short-lived job/registry credential (issued
	// per runner lease, revoked when the run goes terminal). Ephemeral keys are
	// hidden from the API-keys UI, excluded from the MaxAPIKeys quota, and findable
	// by the orphan sweeper.
	Ephemeral  bool       `json:"ephemeral" gorm:"not null;default:false;index"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Revoked    bool       `json:"revoked" gorm:"default:false;not null"`
	CreatedAt  time.Time  `json:"created_at"`
}

// BoundToApp reports whether the key is restricted to a single application. A
// caller enforcing the deploy route uses this to reject a job key acting on any
// app other than the one it was minted for.
func (k *APIKey) BoundToApp(appID uint) bool {
	return k.ApplicationID != nil && *k.ApplicationID == appID
}

// HasScope reports whether the key grants the given scope. An empty scope set
// defaults to read-only; "*" grants everything.
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
