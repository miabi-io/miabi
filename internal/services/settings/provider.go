// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package settings provides cached, typed access to platform-wide key/value
// settings stored in the database. Consumers (auth flow, middleware) read
// runtime configuration through Provider; the admin API writes via the repo and
// calls Invalidate.
package settings

import (
	"strconv"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// cacheTTL bounds how long a provider serves its in-memory cache before lazily
// reloading on the next read. A write Invalidate()s the writer's own cache
// immediately, but other instances (worker process, extra HTTP replicas) only
// learn of a change by re-reading — without a TTL they'd keep resolving their
// boot-time value. 15s keeps overrides near-live at negligible load (the settings
// table is tiny and reads are infrequent).
const cacheTTL = 15 * time.Second

// Well-known setting keys. Only a subset is enforced today; the rest are stored
// and surfaced in the admin UI.
const (
	KeyMaintenanceMode          = "maintenance_mode"
	KeyRequireEmailVerification = "require_email_verification"
	KeyAllowedSignupDomains     = "allowed_signup_domains"
	KeyDefaultWorkspaceRole     = "default_workspace_role"
	KeyMaxWorkspacesPerUser     = "max_workspaces_per_user"
	// KeyMaxWorkspaceMembershipsPerUser caps how many workspaces a user may JOIN
	// as a non-owner member (0 = unlimited).
	KeyMaxWorkspaceMembershipsPerUser = "max_workspace_memberships_per_user"
	KeyAuditLogRetentionDays          = "audit_log_retention_days"
	// Per-application resource caps. 0 = unlimited.
	KeyMaxCPUCores = "max_cpu_cores"
	KeyMaxMemoryMB = "max_memory_mb"

	// KeyExternalBaseDomain is the wildcard base domain for one-click external
	// access (e.g. "apps.example.com", DNS *.apps.example.com). Empty = off.
	// KeyExternalBaseProvider names the Goma certManager provider used for the
	// generated routes ("" = the gateway's default provider).
	KeyExternalBaseDomain   = "external_base_domain"
	KeyExternalBaseProvider = "external_base_provider"

	// KeyCustomLabelsEnabled is the fleet-wide kill-switch for user-defined Docker
	// labels on app containers (Traefik &c.). When false, custom labels are
	// disabled everywhere regardless of any plan capability; when true, the
	// per-plan AllowCustomLabels capability decides. Default true.
	KeyCustomLabelsEnabled = "custom_labels_enabled"
)

// defaults seeds first-boot values. Keys absent here can still be created by the
// admin via the API.
var defaults = []models.Setting{
	{Key: KeyMaintenanceMode, Value: "false", Type: models.SettingTypeBool},
	{Key: KeyRequireEmailVerification, Value: "false", Type: models.SettingTypeBool},
	{Key: KeyAllowedSignupDomains, Value: "", Type: models.SettingTypeString},
	{Key: KeyDefaultWorkspaceRole, Value: "viewer", Type: models.SettingTypeString},
	{Key: KeyMaxWorkspacesPerUser, Value: "3", Type: models.SettingTypeInt},
	{Key: KeyMaxWorkspaceMembershipsPerUser, Value: "3", Type: models.SettingTypeInt},
	{Key: KeyAuditLogRetentionDays, Value: "90", Type: models.SettingTypeInt},
	{Key: KeyMaxCPUCores, Value: "0", Type: models.SettingTypeInt},
	{Key: KeyMaxMemoryMB, Value: "0", Type: models.SettingTypeInt},
	{Key: KeyExternalBaseDomain, Value: "", Type: models.SettingTypeString},
	{Key: KeyExternalBaseProvider, Value: "", Type: models.SettingTypeString},
	{Key: KeyCustomLabelsEnabled, Value: "true", Type: models.SettingTypeBool},
}

// Provider caches settings in memory and exposes typed getters.
type Provider struct {
	repo *repositories.SettingRepository

	mu sync.RWMutex
	// ttl bounds how long the cache is served before a lazy reload; 0 disables
	// auto-refresh (used by tests that inject a fixed cache). loadedAt is when the
	// cache was last filled from the database.
	ttl      time.Duration
	loadedAt time.Time
	cache    map[string]models.Setting
}

// NewProvider builds a provider and loads the cache. Defaults are seeded once
// (missing keys only); existing admin values are never overwritten. envOverrides
// maps setting keys to env-provided values (e.g. external_base_domain from
// MIABI_EXTERNAL_BASE_DOMAIN): a non-empty value is authoritative and re-applied
// on every boot, so the platform can be configured declaratively; an empty value
// leaves the key admin-managed. Pass nil for no overrides.
func NewProvider(repo *repositories.SettingRepository, envOverrides map[string]string) *Provider {
	p := &Provider{repo: repo, cache: map[string]models.Setting{}, ttl: cacheTTL}
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
	// Env-provided values win: force them on every boot so a key like
	// external_base_domain can be set from the environment (12-factor) rather than
	// only the admin UI. Empty values are ignored (the key stays admin-managed).
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
		// Back off: keep the current cache but mark it freshly attempted so a
		// failing database isn't re-queried on every read until the next TTL window.
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

// Invalidate refreshes the cache from the database. Call after a write.
func (p *Provider) Invalidate() { p.reload() }

// refreshIfStale lazily reloads the cache once it is older than the TTL, so an
// override written by another instance is picked up without an explicit
// Invalidate. No-op when the TTL is disabled (ttl <= 0) or the cache is fresh.
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
