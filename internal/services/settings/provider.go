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

// Package settings provides cached, typed access to platform-wide key/value settings stored in the database.
package settings

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Without a TTL other instances would keep resolving their boot-time value.
const cacheTTL = 15 * time.Second

// Only a subset is enforced today; the rest are stored and surfaced in the admin UI.
const (
	KeyMaintenanceMode = "maintenance_mode"
	// Sign-up itself is fixed at boot from MIABI_REGISTRATION_ENABLED; these shape the policy once open.
	KeyRequireEmailVerification = "require_email_verification"
	KeyAllowedSignupDomains     = "allowed_signup_domains"
	KeyDefaultWorkspaceRole     = "default_workspace_role"
	KeyMaxWorkspacesPerUser     = "max_workspaces_per_user"
	// 0 means unlimited.
	KeyMaxWorkspaceMembershipsPerUser = "max_workspace_memberships_per_user"
	KeyWorkspaceCreation              = "workspace_creation"
	// The invitation is the authorization, even while self-service sign-up is closed.
	KeyInvitationSignupEnabled = "invitation_signup_enabled"
	KeyAuditLogRetentionDays   = "audit_log_retention_days"
	// Per-application resource caps. 0 = unlimited.
	KeyMaxCPUCores = "max_cpu_cores"
	KeyMaxMemoryMB = "max_memory_mb"

	// Fleet-wide kill-switch; when true, the per-plan AllowCustomLabels capability decides.
	KeyCustomLabelsEnabled = "custom_labels_enabled"

	// Repo pipelines let the repository choose runner images and commands, so operators can disable them.
	KeyRepoPipelinesEnabled = "repo_pipelines_enabled"

	// Observe is the default because it acts on nothing.
	KeyControlManagerMode = "control_manager_mode"
)

// Joins are unlimited because a user can't join unasked, and member quotas already bound that.
const (
	DefaultMaxWorkspacesPerUser           = 10
	DefaultMaxWorkspaceMembershipsPerUser = -1
	// Changes nothing for accounts that predate it: none of them signed up from an invitation.
	DefaultWorkspaceCreation = models.WorkspaceCreationExceptInvited
)

var defaults = []models.Setting{
	{Key: KeyMaintenanceMode, Value: "false", Type: models.SettingTypeBool},
	{Key: KeyRequireEmailVerification, Value: "false", Type: models.SettingTypeBool},
	{Key: KeyAllowedSignupDomains, Value: "", Type: models.SettingTypeString},
	{Key: KeyDefaultWorkspaceRole, Value: "viewer", Type: models.SettingTypeString},
	{Key: KeyMaxWorkspacesPerUser, Value: strconv.Itoa(DefaultMaxWorkspacesPerUser), Type: models.SettingTypeInt},
	{Key: KeyMaxWorkspaceMembershipsPerUser, Value: strconv.Itoa(DefaultMaxWorkspaceMembershipsPerUser), Type: models.SettingTypeInt},
	{Key: KeyWorkspaceCreation, Value: DefaultWorkspaceCreation, Type: models.SettingTypeString},
	{Key: KeyInvitationSignupEnabled, Value: "true", Type: models.SettingTypeBool},
	{Key: KeyAuditLogRetentionDays, Value: "90", Type: models.SettingTypeInt},
	{Key: KeyMaxCPUCores, Value: "0", Type: models.SettingTypeInt},
	{Key: KeyMaxMemoryMB, Value: "0", Type: models.SettingTypeInt},
	{Key: KeyCustomLabelsEnabled, Value: "true", Type: models.SettingTypeBool},
	{Key: KeyRepoPipelinesEnabled, Value: "true", Type: models.SettingTypeBool},
	{Key: KeyControlManagerMode, Value: "observe", Type: models.SettingTypeString},
}

// Provider caches settings in memory and exposes typed getters.
type Provider struct {
	repo *repositories.SettingRepository

	// Env-supplied keys are re-forced every boot, so the console must not offer them as editable.
	pinned map[string]bool

	mu sync.RWMutex
	// 0 disables auto-refresh, for tests that inject a fixed cache.
	ttl      time.Duration
	loadedAt time.Time
	cache    map[string]models.Setting
}

// NewProvider seeds defaults only for missing keys; non-empty env overrides are re-applied every boot.
func NewProvider(repo *repositories.SettingRepository, envOverrides map[string]string) *Provider {
	p := &Provider{repo: repo, cache: map[string]models.Setting{}, ttl: cacheTTL, pinned: map[string]bool{}}
	for key, val := range envOverrides {
		if strings.TrimSpace(val) != "" {
			p.pinned[key] = true
		}
	}
	p.seed(envOverrides)
	p.reload()
	return p
}

func (p *Provider) seed(envOverrides map[string]string) {
	for _, s := range defaults {
		if err := p.repo.CreateIfMissing(s); err != nil {
			logger.Error("failed to seed setting", "key", s.Key, "error", err)
		}
	}
	var forced []models.Setting
	for key, val := range envOverrides {
		if val == "" {
			continue
		}
		forced = append(forced, models.Setting{Key: key, Value: val, Type: settingType(key)})
	}
	if len(forced) > 0 {
		if err := p.repo.BulkUpsert(forced); err != nil {
			logger.Error("failed to apply env setting overrides", "error", err)
		}
	}
}

// settingType returns the declared type for a known key, defaulting to string.
func settingType(key string) models.SettingType {
	for _, d := range defaults {
		if d.Key == key {
			return d.Type
		}
	}
	return models.SettingTypeString
}

func (p *Provider) reload() {
	if p.repo == nil { // no backing store (tests inject a fixed cache)
		p.mu.Lock()
		p.loadedAt = time.Now()
		p.mu.Unlock()
		return
	}
	all, err := p.repo.All()
	if err != nil {
		logger.Error("failed to load settings", "error", err)
		// Marks the cache freshly attempted so a failing database isn't re-queried on every read.
		p.mu.Lock()
		p.loadedAt = time.Now()
		p.mu.Unlock()
		return
	}
	m := make(map[string]models.Setting, len(all))
	for _, s := range all {
		m[s.Key] = s
	}
	p.mu.Lock()
	p.cache = m
	p.loadedAt = time.Now()
	p.mu.Unlock()
}

func NewFixedProvider(cache map[string]models.Setting) *Provider {
	if cache == nil {
		cache = map[string]models.Setting{}
	}
	return &Provider{cache: cache, pinned: map[string]bool{}}
}

func (p *Provider) Pinned(key string) bool { return p.pinned[key] }

func (p *Provider) PinnedKeys() []string {
	out := make([]string, 0, len(p.pinned))
	for k := range p.pinned {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (p *Provider) Invalidate() { p.reload() }

func (p *Provider) refreshIfStale() {
	p.mu.RLock()
	stale := p.ttl > 0 && time.Since(p.loadedAt) >= p.ttl
	p.mu.RUnlock()
	if stale {
		p.reload()
	}
}

func (p *Provider) raw(key string) (string, bool) {
	p.refreshIfStale()
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.cache[key]
	return s.Value, ok
}

// Bool returns a boolean setting, or def when unset/unparseable.
func (p *Provider) Bool(key string, def bool) bool {
	v, ok := p.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// Int returns an integer setting, or def when unset/unparseable.
func (p *Provider) Int(key string, def int) int {
	v, ok := p.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// String returns a string setting, or def when unset.
func (p *Provider) String(key, def string) string {
	v, ok := p.raw(key)
	if !ok {
		return def
	}
	return v
}
