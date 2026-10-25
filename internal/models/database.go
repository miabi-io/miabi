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

// DBEngine is a supported managed-database engine.
type DBEngine string

const (
	DBEnginePostgres DBEngine = "postgres"
	DBEngineMySQL    DBEngine = "mysql"
	DBEngineMariaDB  DBEngine = "mariadb"
	DBEngineRedis    DBEngine = "redis"
	DBEngineMongoDB  DBEngine = "mongodb"
	// DBEngineLibSQL is a single-database libSQL (sqld) server. Unlike the SQL
	// engines it hosts exactly one database (no user-managed logical databases),
	// has no CLI client (clients speak HTTP/Hrana), and authenticates with a JWT.
	DBEngineLibSQL DBEngine = "libsql"
)

// DBStatus is the lifecycle state of a managed database.
type DBStatus string

const (
	DBStatusProvisioning DBStatus = "provisioning"
	DBStatusRunning      DBStatus = "running"
	DBStatusStopped      DBStatus = "stopped"
	DBStatusFailed       DBStatus = "failed"
	// DBStatusUpgrading marks an instance whose engine version is being upgraded.
	// Lifecycle actions (start/stop/restart/delete) are refused while in this
	// state; the transient Upgrade* fields carry live progress.
	DBStatusUpgrading DBStatus = "upgrading"
)

// UpgradeProgress is the live state of an in-flight version upgrade, surfaced on
// the instance while Status is "upgrading" (and briefly after, on failure).
type UpgradeProgress struct {
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Path        string `json:"path"`  // "in-place" | "dump-restore"
	Phase       string `json:"phase"` // backing-up | stopping-apps | swapping | dumping | restoring | cutover | verifying | starting-apps | done | failed
	Error       string `json:"error,omitempty"`
}

// DatabaseInstance is a workspace-owned database server running as a container. One instance
// hosts one or more logical Databases (SQL engines), so small deployments share a server
// instead of one container per database. Admin credentials are encrypted at rest.
type DatabaseInstance struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_dbinst_workspace_name,unique;not null"`
	// Name is the unique, URL/CLI/docker handle (lowercase [a-z0-9-]) scoped to the
	// workspace.
	Name string `json:"name" gorm:"index:idx_dbinst_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI.
	DisplayName string   `json:"display_name"`
	Engine      DBEngine `json:"engine" gorm:"not null"`
	Version     string   `json:"version" gorm:"not null"`
	Status      DBStatus `json:"status" gorm:"not null;default:provisioning"`
	// ServerID is the node this instance runs on (0 = local control-plane node).
	ServerID  uint `json:"server_id" gorm:"index;not null;default:0"`
	ClusterID uint `json:"cluster_id" gorm:"index;not null;default:0"`
	// ServerName is the display name of the node (transient; populated on read so
	// the UI can show where the instance lives). Empty if the node is unknown.
	ServerName  string `json:"server_name,omitempty" gorm:"-"`
	ContainerID string `json:"container_id,omitempty"`
	// Image is the resolved server image ref (repo:tag), pinned at provision time
	// from the deployment-config catalog. Empty on legacy rows (falls back to the
	// engine default).
	Image string `json:"image,omitempty"`
	// StorageClassName is the operator-registered disk the data volume was created on. Fixed at
	// provision time like a volume's: the data is already there, so it cannot be moved by an edit.
	StorageClassName string `json:"storage_class" gorm:"not null;default:default"`
	VolumeName       string `json:"volume_name,omitempty"`
	// VolumeEngineCreatedAt is the creation timestamp Docker reported for the data volume. A timestamp that
	// moved means the volume was deleted and recreated, so the instance's data is gone even though the row
	// and the volume name are intact.
	VolumeEngineCreatedAt string `json:"volume_engine_created_at,omitempty"`
	// VolumeSizeBytes is the declared capacity of the instance's data volume in
	// bytes (0 = unspecified/unlimited), distinct from the measured SizeBytes
	// below. Recorded at provision time for quota accounting.
	VolumeSizeBytes int64 `json:"volume_size_bytes" gorm:"not null;default:0"`
	// MemoryBytes and NanoCPUs are the container's limits (0 = unlimited). The engine is tuned to the memory
	// limit, and both count against the plan's database budget.
	MemoryBytes int64 `json:"memory_bytes" gorm:"not null;default:0"`
	NanoCPUs    int64 `json:"nano_cpus" gorm:"not null;default:0"`
	// SizeClass names the database size the limits came from (Enterprise); empty for limits set by hand. A label
	// only: editing the size later does not change the instance.
	SizeClass string `json:"size_class,omitempty"`
	// MountPath is the in-container data directory the volume is mounted at
	// (transient; populated on read from the engine spec).
	MountPath string `json:"mount_path,omitempty" gorm:"-"`
	Host      string `json:"host"` // in-network DNS alias
	Port      int    `json:"port"`
	// NetworkName is the Docker network the instance joins — the workspace's default network,
	// shared with its applications so they reach the database by alias. The gateway network is
	// reserved for routed apps. Empty on legacy rows, which fall back to gateway.
	NetworkName string `json:"network_name,omitempty"`
	// Admin (superuser/root) credentials for the server. For Redis this is the
	// requirepass password (AdminUser empty).
	AdminUser        string `json:"admin_user"`
	AdminPasswordEnc string `json:"-" gorm:"column:admin_password_enc"` // encrypted
	// JWTPrivateKeyEnc is the encrypted Ed25519 key used to mint client auth tokens for a libSQL
	// instance; sqld starts with the matching public key (SQLD_AUTH_JWT_KEY), derived from it at
	// bring-up. Empty for every other engine.
	JWTPrivateKeyEnc string `json:"-" gorm:"column:jwt_private_key_enc"` // encrypted

	// SizeBytes is the instance's on-disk size (sum of logical DBs for SQL, used
	// memory for Redis), refreshed by a sync. SizeSyncedAt is when it was last
	// computed.
	SizeBytes    int64      `json:"size_bytes"`
	SizeSyncedAt *time.Time `json:"size_synced_at"`

	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata Metadata `json:"metadata,omitempty" gorm:"serializer:json"`
	// Annotations holds free-form, non-identifying descriptive metadata (the
	// manifest's metadata.annotations); no reserved keys. Persisted as JSON.
	Annotations Metadata  `json:"annotations,omitempty" gorm:"serializer:json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Upgrade carries live progress for a version upgrade, persisted as JSON so it is visible
	// across the API server and the worker running the job and survives a restart. Nil when idle;
	// on failure it lingers with Phase="failed" until the next attempt clears it.
	Upgrade *UpgradeProgress `json:"upgrade,omitempty" gorm:"serializer:json"`

	Databases []Database `json:"databases,omitempty" gorm:"foreignKey:InstanceID"`
	// Networks are the Docker networks the instance's container joins. The
	// workspace's default network is always attached (it carries the alias apps
	// reach the instance by); the user may attach additional custom networks.
	Networks []Network `json:"networks,omitempty" gorm:"many2many:database_instance_networks;"`
}

// EngineSupportsLogicalDatabases reports whether an engine can host named per-app
// databases with their own users (the SQL engines, plus MongoDB which has
// databases with scoped users). Redis cannot.
func EngineSupportsLogicalDatabases(e DBEngine) bool {
	return e == DBEnginePostgres || e == DBEngineMySQL ||
		e == DBEngineMariaDB || e == DBEngineMongoDB
}

// EngineUsesLogicalDatabaseRecord reports whether an engine hangs backups and app connection
// injection off a logical Database row: EngineSupportsLogicalDatabases plus libSQL, which
// gets one implicit row so the per-database pipeline is reused. Only Redis is excluded.
func EngineUsesLogicalDatabaseRecord(e DBEngine) bool {
	return EngineSupportsLogicalDatabases(e) || e == DBEngineLibSQL
}

// SupportsLogicalDatabases reports whether the instance's engine can host named
// per-app databases with their own users.
func (i *DatabaseInstance) SupportsLogicalDatabases() bool {
	return EngineSupportsLogicalDatabases(i.Engine)
}

// NetworkNames returns every Docker network the instance joins: the pinned primary first,
// then the rest. Helper jobs (DDL, size probes, backup, restore) must join the full set or
// risk landing on a network the instance is not on. De-duplicated and never empty.
func (i *DatabaseInstance) NetworkNames(fallback string) []string {
	primary := i.NetworkName
	if primary == "" {
		primary = fallback
	}
	out := make([]string, 0, len(i.Networks)+1)
	seen := map[string]bool{}
	if primary != "" {
		out = append(out, primary)
		seen[primary] = true
	}
	for _, n := range i.Networks {
		if n.DockerName == "" || seen[n.DockerName] {
			continue
		}
		seen[n.DockerName] = true
		out = append(out, n.DockerName)
	}
	if len(out) == 0 {
		return []string{fallback}
	}
	return out
}

// SwarmScoped reports whether name is one of the instance's overlay networks.
func (i *DatabaseInstance) SwarmScoped(name string) bool {
	for n := range i.Networks {
		if i.Networks[n].DockerName == name {
			return i.Networks[n].SwarmScoped()
		}
	}
	return false
}

// Database is a logical database hosted on a DatabaseInstance, with its own
// dedicated user (privileges scoped to this database). The password is
// encrypted at rest. Optionally owned by an application.
type Database struct {
	UIDModel
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index;not null"`
	InstanceID  uint   `json:"instance_id" gorm:"index:idx_db_instance_name,unique;not null"`
	Name        string `json:"name" gorm:"index:idx_db_instance_name,unique;not null"` // db name on the server
	Username    string `json:"username" gorm:"not null"`
	PasswordEnc string `json:"-" gorm:"column:password_enc"` // encrypted
	// ReadOnlyUsername is the database's optional read-only user (Postgres): SELECT on every table, now
	// and future, for consumers that must not write. Empty when none. Its password is encrypted like
	// PasswordEnc.
	ReadOnlyUsername    string   `json:"readonly_username,omitempty"`
	ReadOnlyPasswordEnc string   `json:"-" gorm:"column:readonly_password_enc"`
	Status              DBStatus `json:"status" gorm:"not null;default:provisioning"`
	ApplicationID       *uint    `json:"application_id,omitempty" gorm:"index"` // optional owner
	// Application declares the FK so deleting the owning app detaches this
	// database (ON DELETE SET NULL) instead of dangling. Not serialized.
	Application *Application `json:"-" gorm:"foreignKey:ApplicationID;constraint:OnDelete:SET NULL"`
	// EnvPrefix namespaces the connection env vars injected into the owning app
	// (e.g. "ANALYTICS" -> ANALYTICS_DB_URL); empty = unprefixed (DB_*).
	// Lets one app hold several databases without clobbering.
	EnvPrefix string `json:"env_prefix,omitempty"`
	// EnvMap renames injected vars per connection field (url, host, port, name,
	// user, password, database_url); an empty name skips the field.
	EnvMap map[string]string `json:"env_map,omitempty" gorm:"serializer:json"`
	// EnvVars records the var names injected into the owning app, so a detach
	// removes exactly those. Empty on links made before it was recorded.
	EnvVars []string `json:"env_vars,omitempty" gorm:"serializer:json"`
	// SizeBytes is the database's on-disk size, refreshed by a sync.
	SizeBytes    int64      `json:"size_bytes"`
	SizeSyncedAt *time.Time `json:"size_synced_at"`
	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata  Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DatabaseInstanceLink attaches an instance with no per-app logical databases
// (Redis) to an application. Unlike a logical Database, an instance can be
// linked by many apps, each with its own env prefix and mapping.
type DatabaseInstanceLink struct {
	ID            uint `json:"id" gorm:"primaryKey"`
	WorkspaceID   uint `json:"workspace_id" gorm:"index;not null"`
	InstanceID    uint `json:"instance_id" gorm:"uniqueIndex:idx_dblink_inst_app;not null"`
	ApplicationID uint `json:"application_id" gorm:"uniqueIndex:idx_dblink_inst_app;index;not null"`
	// Application declares the FK so deleting the app drops its links.
	Application *Application      `json:"-" gorm:"foreignKey:ApplicationID;constraint:OnDelete:CASCADE"`
	EnvPrefix   string            `json:"env_prefix,omitempty"`
	EnvMap      map[string]string `json:"env_map,omitempty" gorm:"serializer:json"`
	EnvVars     []string          `json:"env_vars,omitempty" gorm:"serializer:json"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}
