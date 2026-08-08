// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package platformimage is the deployment-config image catalog: the single source of truth for every
// container image the platform itself runs. Built-in defaults live in code; admins override them at runtime
// via the settings store, and an optional global registry mirror repoints all of them at a private registry.
package platformimage

import (
	"strings"
)

// Image catalog keys.
const (
	KeyPostgres = "db.postgres"
	KeyMySQL    = "db.mysql"
	KeyMariaDB  = "db.mariadb"
	KeyRedis    = "db.redis"
	KeyMongoDB  = "db.mongodb"
	KeyLibSQL   = "db.libsql"

	KeyBackupPostgres = "backup.postgres"
	KeyBackupMySQL    = "backup.mysql"
	KeyBackupMongoDB  = "backup.mongodb"
	KeyBackupLibSQL   = "backup.libsql"
	KeyBackupVolume   = "backup.volume"

	KeyGoma     = "gateway.goma"
	KeyRelay    = "util.relay"
	KeyHelper   = "util.helper"
	KeyAgent    = "agent"
	KeyRegistry = "util.registry"

	// KeyPack is the one-shot helper image that runs the `pack` CLI to build an
	// app from source with Cloud Native Buildpacks. KeyBuildpackBuilder is the
	// default builder image pack uses when an app does not override it.
	KeyPack             = "build.pack"
	KeyBuildpackBuilder = "build.buildpack-builder"

	settingPrefix = "image."
	keyMirror     = "image.registry_mirror"
)

// Entry describes a platform image: a stable key, a human label, a category, the
// built-in default ref, and what it's used for.
type Entry struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Category    string `json:"category"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

// baseCatalog is the static set of platform images with their built-in defaults.
// Gateway/relay defaults are seeded from config at construction (see New).
func baseCatalog() []Entry {
	return []Entry{
		{KeyPostgres, "PostgreSQL", "Databases", "postgres:17-alpine", "Managed PostgreSQL server"},
		{KeyMySQL, "MySQL", "Databases", "mysql:8.4", "Managed MySQL server"},
		{KeyMariaDB, "MariaDB", "Databases", "mariadb:11", "Managed MariaDB server"},
		{KeyRedis, "Redis", "Databases", "redis:7-alpine", "Managed Redis server"},
		{KeyMongoDB, "MongoDB", "Databases", "mongo:7.0", "Managed MongoDB server"},
		{KeyLibSQL, "libSQL", "Databases", "ghcr.io/tursodatabase/libsql-server:latest", "Managed libSQL (sqld) server"},
		{KeyBackupPostgres, "PostgreSQL backup (pg-bkup)", "Backups", "jkaninda/pg-bkup:latest", "Backup/restore tool for PostgreSQL"},
		{KeyBackupMySQL, "MySQL/MariaDB backup (mysql-bkup)", "Backups", "jkaninda/mysql-bkup:latest", "Backup/restore tool for MySQL/MariaDB"},
		{KeyBackupMongoDB, "MongoDB backup (mongodb-bkup)", "Backups", "jkaninda/mongodb-bkup:latest", "Backup/restore tool for MongoDB"},
		{KeyBackupLibSQL, "libSQL backup (libsql-bkup)", "Backups", "jkaninda/libsql-bkup:latest", "Backup/restore tool for libSQL"},
		{KeyBackupVolume, "Volume backup (volume-bkup)", "Backups", "jkaninda/volume-bkup:latest", "Backup/restore tool for Docker volumes"},
		{KeyGoma, "Goma Gateway", "Gateway", "jkaninda/goma-gateway:latest", "Reverse proxy / edge gateway"},
		{KeyRelay, "Port-forward relay (socat)", "Internal", "alpine/socat:latest", "Bridges on-demand database port-forwards"},
		{KeyRegistry, "Docker registry (distribution)", "Internal", "registry:3", "Built-in multi-tenant container registry"},
		{KeyHelper, "Volume helper (busybox)", "Internal", "busybox:1.36", "Seeds config/data volumes"},
		{KeyPack, "Buildpacks (pack CLI)", "Build", "miabi/pack:latest", "Builds apps from source with Cloud Native Buildpacks"},
		{KeyBuildpackBuilder, "Buildpacks builder", "Build", "paketobuildpacks/builder-jammy-base", "Default CNB builder image used by pack"},
		{KeyAgent, "Node agent", "Agent", "miabi/agent:latest", "Shown in the node join command"},
	}
}

// Store reads string settings with a default. Satisfied by settings.Provider.
type Store interface {
	String(key, def string) string
}

// Resolver resolves a catalog key to an effective image ref (admin override ->
// built-in default), applying the global registry mirror.
type Resolver struct {
	store    Store
	defaults map[string]Entry
}

// New builds a resolver. configDefaults overrides built-in defaults for specific
// keys (e.g. the gateway/relay images from env config), so env-based config
// keeps acting as the default with admin overrides layered on top.
func New(store Store, configDefaults map[string]string) *Resolver {
	defaults := map[string]Entry{}
	for _, e := range baseCatalog() {
		if d, ok := configDefaults[e.Key]; ok && strings.TrimSpace(d) != "" {
			e.Default = d
		}
		defaults[e.Key] = e
	}
	return &Resolver{store: store, defaults: defaults}
}

// Ref returns the effective image ref for a catalog key.
func (r *Resolver) Ref(key string) string {
	def := ""
	if e, ok := r.defaults[key]; ok {
		def = e.Default
	}
	val := r.store.String(settingPrefix+key, def)
	if strings.TrimSpace(val) == "" { // unset or cleared override -> default
		val = def
	}
	return r.applyMirror(val)
}

// Mirror returns the configured registry mirror prefix (empty if unset).
func (r *Resolver) Mirror() string {
	return strings.TrimRight(strings.TrimSpace(r.store.String(keyMirror, "")), "/")
}

func (r *Resolver) applyMirror(ref string) string {
	mirror := r.Mirror()
	if mirror == "" || ref == "" || isQualified(ref) {
		return ref
	}
	return mirror + "/" + ref
}

// CatalogItem is a catalog entry resolved against the current settings, for the
// admin API.
type CatalogItem struct {
	Entry
	Override  string `json:"override"`  // raw admin override ("" = none)
	Effective string `json:"effective"` // what the platform will actually run
}

// Catalog returns every image with its default, override, and effective ref,
// ordered as defined.
func (r *Resolver) Catalog() []CatalogItem {
	out := make([]CatalogItem, 0, len(r.defaults))
	for _, e := range baseCatalog() {
		def := r.defaults[e.Key].Default
		e.Default = def
		out = append(out, CatalogItem{
			Entry:     e,
			Override:  r.store.String(settingPrefix+e.Key, ""),
			Effective: r.Ref(e.Key),
		})
	}
	return out
}

// ValidKey reports whether key is a known catalog key.
func (r *Resolver) ValidKey(key string) bool {
	_, ok := r.defaults[key]
	return ok
}

// SettingKey is the settings-store key for a catalog key's override.
func SettingKey(key string) string { return settingPrefix + key }

// MirrorSettingKey is the settings-store key for the registry mirror.
func MirrorSettingKey() string { return keyMirror }

// RepoTag splits an image ref into its repository and tag. A missing tag
// defaults to "latest". Handles a registry host:port (the colon before a port
// is not the tag separator).
func RepoTag(ref string) (repo, tag string) {
	lastSlash := strings.LastIndex(ref, "/")
	lastColon := strings.LastIndex(ref, ":")
	if lastColon > lastSlash { // a colon in the final path segment = the tag
		return ref[:lastColon], ref[lastColon+1:]
	}
	return ref, "latest"
}

// isQualified reports whether a ref's first path segment looks like a registry
// host (contains a "." or ":"), in which case the mirror prefix is not applied.
func isQualified(ref string) bool {
	i := strings.Index(ref, "/")
	if i < 0 {
		return false // single segment (e.g. "postgres:17") -> not qualified
	}
	return strings.ContainsAny(ref[:i], ".:")
}
