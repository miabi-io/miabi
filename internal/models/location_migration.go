// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// MigrationStatus is where a location migration stands.
type MigrationStatus string

const (
	MigrationRunning MigrationStatus = "running"
	// MigrationAwaitingCutover: the data is pre-copied and the app still serves at the source, waiting
	// for someone to choose the moment of downtime.
	MigrationAwaitingCutover MigrationStatus = "awaiting_cutover"
	// MigrationCutOver: the app serves at the target; the source copy is kept until finalize, so the
	// move can still be rolled back.
	MigrationCutOver    MigrationStatus = "cut_over"
	MigrationFinalized  MigrationStatus = "finalized"
	MigrationRolledBack MigrationStatus = "rolled_back"
	MigrationFailed     MigrationStatus = "failed"
	MigrationCancelled  MigrationStatus = "cancelled"
)

// Terminal reports whether nothing further can happen to the migration.
func (s MigrationStatus) Terminal() bool {
	switch s {
	case MigrationFinalized, MigrationRolledBack, MigrationFailed, MigrationCancelled:
		return true
	}
	return false
}

// Migration phases, in order. A run records the phase it enters, so a resumed run starts there.
const (
	MigrationPhasePrepare   = "prepare"
	MigrationPhasePresync   = "presync"
	MigrationPhaseStop      = "stop"
	MigrationPhaseFinalSync = "final_sync"
	MigrationPhaseSwitch    = "switch"
	MigrationPhaseVerify    = "verify"
	MigrationPhaseReroute   = "reroute"
	MigrationPhaseDone      = "done"
	MigrationPhaseRollback  = "rollback"
	MigrationPhaseFinalize  = "finalize"
)

// Database strategies.
const (
	// DBStrategyMove relocates the instance itself; only for an instance no other app uses.
	DBStrategyMove = "move"
	// DBStrategyNewInstance provisions an instance at the target and restores the app's databases into it.
	DBStrategyNewInstance = "new_instance"
	// DBStrategyExistingInstance restores the app's databases into an instance already at the target.
	DBStrategyExistingInstance = "existing_instance"
)

// Volume actions.
const (
	VolumeActionCopy      = "copy"      // the data is copied with rsync
	VolumeActionRedeclare = "redeclare" // nfs/cifs: the data is on the share, only the declaration moves
)

// Cutover modes.
const (
	CutoverAuto   = "auto"
	CutoverManual = "manual"
)

// LocationMigration moves one application, with its volumes and databases, to another location. The row
// is the run's only state: progress, what was created at the target and what the source looked like are
// all here, so a restarted worker resumes and a rollback knows what to put back.
type LocationMigration struct {
	ID              uint            `json:"id" gorm:"primaryKey"`
	WorkspaceID     uint            `json:"workspace_id" gorm:"index;not null"`
	ApplicationID   uint            `json:"application_id" gorm:"index;not null"`
	AppName         string          `json:"app_name"`
	SourceClusterID uint            `json:"source_cluster_id"`
	SourceServerID  uint            `json:"source_server_id"`
	TargetClusterID uint            `json:"target_cluster_id"`
	TargetServerID  uint            `json:"target_server_id"`
	Status          MigrationStatus `json:"status" gorm:"index;not null;default:running"`
	Phase           string          `json:"phase"`
	CutoverMode     string          `json:"cutover_mode" gorm:"not null;default:auto"`
	BandwidthKBps   int             `json:"bandwidth_kbps"`
	// CutoverRequested resumes a manual migration waiting at the cutover point.
	CutoverRequested bool `json:"cutover_requested"`
	// CancelRequested stops a run at its next checkpoint; only honoured before the app goes down.
	CancelRequested bool `json:"cancel_requested"`

	Plan     MigrationPlan     `json:"plan" gorm:"serializer:json"`
	State    MigrationState    `json:"-" gorm:"serializer:json"`
	Progress MigrationProgress `json:"progress" gorm:"serializer:json"`
	Report   BundleReport      `json:"report" gorm:"serializer:json"`
	Error    string            `json:"error,omitempty" gorm:"type:text"`

	// LeaseUntil is held by the worker running the migration; a sweep resumes a run whose lease lapsed.
	LeaseUntil *time.Time `json:"-" gorm:"index"`

	StartedByID   *uint      `json:"started_by_id,omitempty"`
	DowntimeAt    *time.Time `json:"downtime_at,omitempty"`
	CutoverAt     *time.Time `json:"cutover_at,omitempty"`
	FinalizeAfter *time.Time `json:"finalize_after,omitempty" gorm:"index"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// MigrationIssue is a blocker or a warning the planner found.
type MigrationIssue struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Resource string `json:"resource,omitempty"`
}

// MigrationPlan is the dry-run result a person confirms, and what the run then executes.
type MigrationPlan struct {
	Location        string            `json:"location"`
	LocationLabel   string            `json:"location_label,omitempty"`
	TargetClusterID uint              `json:"target_cluster_id"`
	TargetServerID  uint              `json:"target_server_id"`
	Volumes         []MigrationVolume `json:"volumes"`
	Databases       []MigrationDBItem `json:"databases"`
	Blockers        []MigrationIssue  `json:"blockers"`
	Warnings        []MigrationIssue  `json:"warnings"`
	GeneratedURLs   []string          `json:"generated_urls,omitempty"`
	CustomDomains   []string          `json:"custom_domains,omitempty"`
	CopyBytes       int64             `json:"copy_bytes"`
	Service         bool              `json:"service"`
}

// MigrationVolume is one volume the app mounts.
type MigrationVolume struct {
	VolumeID     uint   `json:"volume_id"`
	Name         string `json:"name"`
	Driver       string `json:"driver"`
	Action       string `json:"action"`
	UsedBytes    int64  `json:"used_bytes"`
	StorageClass string `json:"storage_class"`
}

// MigrationDBItem is one database instance the app depends on, and what happens to it.
type MigrationDBItem struct {
	InstanceID       uint                 `json:"instance_id"`
	InstanceName     string               `json:"instance_name"`
	Engine           DBEngine             `json:"engine"`
	Version          string               `json:"version"`
	Exclusive        bool                 `json:"exclusive"`
	Strategies       []string             `json:"strategies"`
	Strategy         string               `json:"strategy"`
	TargetInstanceID uint                 `json:"target_instance_id,omitempty"`
	Databases        []MigrationLogicalDB `json:"databases"`
	SizeBytes        int64                `json:"size_bytes"`
}

// MigrationLogicalDB is one of the app's logical databases on an instance.
type MigrationLogicalDB struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	EnvPrefix string `json:"env_prefix,omitempty"`
}

// MigrationState is what the run created and what the source looked like before it: everything rollback
// and finalize need, so neither has to guess.
type MigrationState struct {
	SourceContainerID string                 `json:"source_container_id,omitempty"`
	SourceReleaseID   uint                   `json:"source_release_id,omitempty"`
	Volumes           []MigrationVolumeState `json:"volumes,omitempty"`
	Instances         []MigrationMoveState   `json:"instances,omitempty"`
	Restores          []MigrationRestore     `json:"restores,omitempty"`
	// AppStopped records that the app was stopped at the source, so a resumed run does not treat the
	// downtime as not yet started.
	AppStopped   bool `json:"app_stopped,omitempty"`
	Switched     bool `json:"switched,omitempty"`
	DeploymentID uint `json:"deployment_id,omitempty"`
	// SourceGateway is the edge node that served the app before, told to drop it once it moves.
	SourceGateway uint `json:"source_gateway,omitempty"`
}

// MigrationVolumeState is one volume's source and target.
type MigrationVolumeState struct {
	VolumeID           uint   `json:"volume_id"`
	DockerName         string `json:"docker_name"`
	SourceServerID     uint   `json:"source_server_id"`
	SourceClusterID    uint   `json:"source_cluster_id"`
	SourceCreatedAt    string `json:"source_created_at"`
	SourceMountpoint   string `json:"source_mountpoint"`
	SourceStorageClass string `json:"source_storage_class"`
	TargetCreatedAt    string `json:"target_created_at,omitempty"`
	TargetMountpoint   string `json:"target_mountpoint,omitempty"`
	TargetStorageClass string `json:"target_storage_class,omitempty"`
	Prepared           bool   `json:"prepared,omitempty"`
}

// MigrationMoveState is one database instance moved whole.
type MigrationMoveState struct {
	InstanceID         uint   `json:"instance_id"`
	VolumeName         string `json:"volume_name"`
	SourceServerID     uint   `json:"source_server_id"`
	SourceClusterID    uint   `json:"source_cluster_id"`
	SourceContainerID  string `json:"source_container_id"`
	SourceCreatedAt    string `json:"source_created_at"`
	SourceStorageClass string `json:"source_storage_class"`
	SourceStatus       string `json:"source_status"`
	TargetCreatedAt    string `json:"target_created_at,omitempty"`
	TargetStorageClass string `json:"target_storage_class,omitempty"`
	// TargetContainerID is the container the instance ran in at the target, kept for cleanup after a rollback.
	TargetContainerID string `json:"target_container_id,omitempty"`
	Prepared          bool   `json:"prepared,omitempty"`
}

// MigrationRestore is one logical database restored into another instance at the target.
type MigrationRestore struct {
	SourceDatabaseID uint   `json:"source_database_id"`
	SourceInstanceID uint   `json:"source_instance_id"`
	Name             string `json:"name"`
	EnvPrefix        string `json:"env_prefix,omitempty"`
	TargetInstanceID uint   `json:"target_instance_id,omitempty"`
	TargetDatabaseID uint   `json:"target_database_id,omitempty"`
	// CreatedInstance marks an instance this migration provisioned, removed again on rollback.
	CreatedInstance bool `json:"created_instance,omitempty"`
	Restored        bool `json:"restored,omitempty"`
}

// MigrationProgress is the live view of a run.
type MigrationProgress struct {
	Items []MigrationProgressItem `json:"items,omitempty"`
	// Pass is the current pre-copy pass.
	Pass int `json:"pass,omitempty"`
	// LastDeltaBytes is what the last pass had to send: the final pass faces roughly this much.
	LastDeltaBytes int64 `json:"last_delta_bytes"`
	// BytesPerSecond is the throughput the last pass achieved, for the downtime estimate.
	BytesPerSecond int64 `json:"bytes_per_second"`
	// EstimatedDowntimeSeconds is the final pass plus a deploy, from the last pass's numbers.
	EstimatedDowntimeSeconds int    `json:"estimated_downtime_seconds"`
	Message                  string `json:"message,omitempty"`
}

// MigrationProgressItem is one volume or database.
type MigrationProgressItem struct {
	Kind   string `json:"kind"` // volume | database
	Name   string `json:"name"`
	Status string `json:"status"` // pending | copying | synced | done | failed
	Bytes  int64  `json:"bytes"`
	Total  int64  `json:"total,omitempty"`
	Delta  int64  `json:"delta,omitempty"`
	Passes int    `json:"passes,omitempty"`
	Detail string `json:"detail,omitempty"`
}
