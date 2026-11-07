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
	// DBEngineLibSQL hosts exactly one database, has no CLI client (HTTP/Hrana only), and authenticates with a JWT.
	DBEngineLibSQL DBEngine = "libsql"
)

// DBStatus is the lifecycle state of a managed database.
type DBStatus string

const (
	DBStatusProvisioning DBStatus = "provisioning"
	DBStatusRunning      DBStatus = "running"
	DBStatusStopped      DBStatus = "stopped"
	DBStatusFailed       DBStatus = "failed"
	// DBStatusUpgrading refuses lifecycle actions while a version upgrade runs.
	DBStatusUpgrading DBStatus = "upgrading"
)

type UpgradeProgress struct {
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Path        string `json:"path"`  // "in-place" | "dump-restore"
	Phase       string `json:"phase"` // backing-up | stopping-apps | swapping | dumping | restoring | cutover | verifying | starting-apps | done | failed
	Error       string `json:"error,omitempty"`
}

// DatabaseInstance is a database server container hosting one or more logical Databases; admin credentials are encrypted.
type DatabaseInstance struct {
	UIDModel
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index:idx_dbinst_workspace_name,unique;not null"`
	Name        string `json:"name" gorm:"index:idx_dbinst_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI.
	DisplayName string   `json:"display_name"`
	Engine      DBEngine `json:"engine" gorm:"not null"`
	Version     string   `json:"version" gorm:"not null"`
	Status      DBStatus `json:"status" gorm:"not null;default:provisioning"`
	// ServerID is the node this instance runs on (0 = local control-plane node).
	ServerID    uint   `json:"server_id" gorm:"index;not null;default:0"`
	ClusterID   uint   `json:"cluster_id" gorm:"index;not null;default:0"`
	ServerName  string `json:"server_name,omitempty" gorm:"-"`
	ContainerID string `json:"container_id,omitempty"`
	// Empty on legacy rows, which fall back to the engine default.
	Image string `json:"image,omitempty"`
	// Fixed at provision time: the data is already on that disk, so an edit cannot move it.
	StorageClassName string `json:"storage_class" gorm:"not null;default:default"`
	VolumeName       string `json:"volume_name,omitempty"`
	// A changed timestamp means the volume was recreated, so the data is gone even though the name is intact.
	VolumeEngineCreatedAt string `json:"volume_engine_created_at,omitempty"`
	// Declared capacity for quota accounting (0 = unlimited), distinct from the measured SizeBytes.
	VolumeSizeBytes int64 `json:"volume_size_bytes" gorm:"not null;default:0"`
	// Container limits (0 = unlimited); the engine is tuned to the memory limit and both count against the plan budget.
	MemoryBytes int64 `json:"memory_bytes" gorm:"not null;default:0"`
	NanoCPUs    int64 `json:"nano_cpus" gorm:"not null;default:0"`
	// A label only: editing the size later does not change the instance.
	SizeClass string `json:"size_class,omitempty"`
	MountPath string `json:"mount_path,omitempty" gorm:"-"`
	Host      string `json:"host"` // in-network DNS alias
	Port      int    `json:"port"`
	// The workspace default network, shared with apps so they reach the DB by alias; empty on legacy rows means gateway.
	NetworkName string `json:"network_name,omitempty"`
	// For Redis this is the requirepass password and AdminUser is empty.
	AdminUser        string `json:"admin_user"`
	AdminPasswordEnc string `json:"-" gorm:"column:admin_password_enc"` // encrypted
	// Ed25519 key that mints libSQL client tokens; sqld gets the derived public key. Empty for other engines.
	JWTPrivateKeyEnc string `json:"-" gorm:"column:jwt_private_key_enc"` // encrypted

	// Sum of logical DBs for SQL engines, used memory for Redis.
	SizeBytes    int64      `json:"size_bytes"`
	SizeSyncedAt *time.Time `json:"size_synced_at"`

	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata    Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	Annotations Metadata  `json:"annotations,omitempty" gorm:"serializer:json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Persisted so the API server and worker share progress across restarts; lingers with Phase=failed until the next attempt.
	Upgrade *UpgradeProgress `json:"upgrade,omitempty" gorm:"serializer:json"`

	Databases []Database `json:"databases,omitempty" gorm:"foreignKey:InstanceID"`
	// The workspace default network is always attached since it carries the alias apps use.
	Networks []Network `json:"networks,omitempty" gorm:"many2many:database_instance_networks;"`
}

// EngineSupportsLogicalDatabases reports whether an engine hosts named databases with scoped users (not Redis).
func EngineSupportsLogicalDatabases(e DBEngine) bool {
	return e == DBEnginePostgres || e == DBEngineMySQL ||
		e == DBEngineMariaDB || e == DBEngineMongoDB
}

// EngineUsesLogicalDatabaseRecord also includes libSQL, which gets one implicit row to reuse the per-database pipeline.
func EngineUsesLogicalDatabaseRecord(e DBEngine) bool {
	return EngineSupportsLogicalDatabases(e) || e == DBEngineLibSQL
}

func (i *DatabaseInstance) SupportsLogicalDatabases() bool {
	return EngineSupportsLogicalDatabases(i.Engine)
}

// NetworkNames returns all networks, primary first; helper jobs must join the full set to reach the instance.
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

// Database is a logical database on a DatabaseInstance with its own scoped user, optionally owned by an application.
type Database struct {
	UIDModel
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index;not null"`
	InstanceID  uint   `json:"instance_id" gorm:"index:idx_db_instance_name,unique;not null"`
	Name        string `json:"name" gorm:"index:idx_db_instance_name,unique;not null"` // db name on the server
	Username    string `json:"username" gorm:"not null"`
	PasswordEnc string `json:"-" gorm:"column:password_enc"` // encrypted
	// Optional Postgres read-only user with SELECT on all current and future tables; empty when none.
	ReadOnlyUsername    string   `json:"readonly_username,omitempty"`
	ReadOnlyPasswordEnc string   `json:"-" gorm:"column:readonly_password_enc"`
	Status              DBStatus `json:"status" gorm:"not null;default:provisioning"`
	ApplicationID       *uint    `json:"application_id,omitempty" gorm:"index"` // optional owner
	// ON DELETE SET NULL so deleting the owning app detaches the database instead of dangling.
	Application *Application `json:"-" gorm:"foreignKey:ApplicationID;constraint:OnDelete:SET NULL"`
	// Prefixes injected env vars (e.g. ANALYTICS_DB_URL) so one app can hold several databases; empty means DB_*.
	EnvPrefix string `json:"env_prefix,omitempty"`
	// Renames injected vars per connection field; an empty name skips the field.
	EnvMap map[string]string `json:"env_map,omitempty" gorm:"serializer:json"`
	// Lets a detach remove exactly the injected vars; empty on links made before it was recorded.
	EnvVars []string `json:"env_vars,omitempty" gorm:"serializer:json"`
	// SizeBytes is the database's on-disk size, refreshed by a sync.
	SizeBytes    int64      `json:"size_bytes"`
	SizeSyncedAt *time.Time `json:"size_synced_at"`
	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata  Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DatabaseInstanceLink attaches a Redis-style instance to an app; unlike a Database, many apps may link one instance.
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
