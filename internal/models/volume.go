// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"time"
)

// VolumeAccessMode is how many nodes can mount a volume read-write at once.
type VolumeAccessMode string

const (
	// AccessRWO (ReadWriteOnce): a node-local volume usable by one node at a time
	// (the default "local" driver). A replicated service must NOT mount one, or
	// each node would silently get its own empty copy.
	AccessRWO VolumeAccessMode = "rwo"
	// AccessRWX (ReadWriteMany): a shared volume (NFS/CIFS/Ceph) every replica can
	// mount at once, so a stateful service can run with replicas > 1.
	AccessRWX VolumeAccessMode = "rwx"
)

// ValidVolumeAccessMode reports whether m is a known access mode.
func ValidVolumeAccessMode(m VolumeAccessMode) bool {
	return m == AccessRWO || m == AccessRWX
}

// Volume driver names Miabi understands. The NFS/CIFS variants use Docker's
// built-in local driver with mount options (no external plugin required).
const (
	VolumeDriverLocal = "local"
	VolumeDriverNFS   = "nfs"
	VolumeDriverCIFS  = "cifs"
	// VolumeDriverHost is a bind to an operator-managed host path (under /mnt/*), not a Docker
	// volume. It is for storage mounted at the SAME path on every node, so a replicated service
	// can share it; AccessMode is rwx. Privileged workspaces only; HostPath carries the path.
	VolumeDriverHost = "host"
)

// Volume is a workspace-owned, managed Docker volume for persistent data.
type Volume struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_vol_workspace_name,unique;not null"`
	// Name is the unique, URL/CLI/docker handle (lowercase [a-z0-9-]) scoped to the
	// workspace. Renamed from the former "slug".
	Name string `json:"name" gorm:"index:idx_vol_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI. Renamed from "name".
	DisplayName string `json:"display_name"`
	DockerName  string `json:"docker_name" gorm:"uniqueIndex;not null"`
	// ServerID is the node this volume lives on (0 = local control-plane node).
	ServerID uint `json:"server_id" gorm:"index;not null;default:0"`
	// ServerName is the display name of the node (transient; populated on read).
	ServerName string `json:"server_name,omitempty" gorm:"-"`
	Mountpoint string `json:"mountpoint,omitempty"`
	// SizeBytes is the declared capacity / size limit of the volume in bytes
	// (0 = unspecified/unlimited). Recorded at create time and used for quota
	// accounting; hard enforcement depends on the node's storage backend.
	SizeBytes int64 `json:"size_bytes" gorm:"not null;default:0"`
	// UsedBytes is the last MEASURED on-disk usage (docker system df), distinct
	// from the declared SizeBytes. 0 with nil UsedMeasuredAt = never measured.
	UsedBytes      int64      `json:"used_bytes" gorm:"not null;default:0"`
	UsedMeasuredAt *time.Time `json:"used_measured_at,omitempty"`
	// Imported marks a volume that references a pre-existing external Docker volume
	// by its own name (DockerName = the existing name; no data was moved/created).
	Imported bool `json:"imported" gorm:"not null;default:false"`

	// Shared storage (cluster mode). Driver is the Docker volume driver: "local"
	// (default, node-local) or "nfs"/"cifs" (a backend a replicated service can
	// share across nodes). AccessMode follows from it: local => rwo, shared => rwx.
	Driver string `json:"driver" gorm:"not null;default:local"`
	// AccessMode is rwo (node-local, single node) or rwx (shared, multi-node). The
	// guardrails use it to refuse replicating an app backed by an rwo volume.
	AccessMode VolumeAccessMode `json:"access_mode" gorm:"not null;default:rwo"`
	// DriverOptsEnc is the encrypted JSON of the driver's mount options (which may
	// include a CIFS password). Set at create time, never returned. The volume is
	// immutable, so options can't be edited after creation.
	DriverOptsEnc string `json:"-" gorm:"type:text"`
	// HostPath is the operator-managed host directory a "host" driver volume binds
	// (under /mnt/*). Empty for every other driver. Not a secret — it is the bind
	// source, mounted directly into the app's containers on each node.
	HostPath string `json:"host_path,omitempty"`
	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata Metadata `json:"metadata,omitempty" gorm:"serializer:json"`
	// Annotations holds free-form, non-identifying descriptive metadata (the
	// manifest's metadata.annotations); no reserved keys. Persisted as JSON.
	Annotations Metadata  `json:"annotations,omitempty" gorm:"serializer:json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
