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

// VolumeAccessMode is how many nodes can mount a volume read-write at once.
type VolumeAccessMode string

const (
	// A replicated service must not mount one, or each node silently gets its own empty copy.
	AccessRWO VolumeAccessMode = "rwo"
	AccessRWX VolumeAccessMode = "rwx"
)

// ValidVolumeAccessMode reports whether m is a known access mode.
func ValidVolumeAccessMode(m VolumeAccessMode) bool {
	return m == AccessRWO || m == AccessRWX
}

// NFS/CIFS use Docker's built-in local driver with mount options, no plugin required.
const (
	VolumeDriverLocal = "local"
	VolumeDriverNFS   = "nfs"
	VolumeDriverCIFS  = "cifs"
	// A bind to an operator path mounted identically on every node, so replicas can share it; privileged workspaces only.
	VolumeDriverHost = "host"
)

// Volume is a workspace-owned, managed Docker volume for persistent data.
type Volume struct {
	UIDModel
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index:idx_vol_workspace_name,unique;not null"`
	Name        string `json:"name" gorm:"index:idx_vol_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI.
	DisplayName string `json:"display_name"`
	DockerName  string `json:"docker_name" gorm:"uniqueIndex;not null"`
	// ServerID is the node this volume lives on (0 = local control-plane node).
	ServerID  uint `json:"server_id" gorm:"index;not null;default:0"`
	ClusterID uint `json:"cluster_id" gorm:"index;not null;default:0"`
	// ServerName is the display name of the node (transient; populated on read).
	ServerName      string     `json:"server_name,omitempty" gorm:"-"`
	Mountpoint      string     `json:"-"`
	EngineCreatedAt string     `json:"engine_created_at,omitempty"`
	SizeBytes       int64      `json:"size_bytes" gorm:"not null;default:0"`
	UsedBytes       int64      `json:"used_bytes" gorm:"not null;default:0"`
	UsedMeasuredAt  *time.Time `json:"used_measured_at,omitempty"`
	Imported        bool       `json:"imported" gorm:"not null;default:false"`
	// AccessMode follows from the driver: local is rwo, shared is rwx.
	Driver string `json:"driver" gorm:"not null;default:local"`
	// The guardrails refuse replicating an app backed by an rwo volume.
	AccessMode VolumeAccessMode `json:"access_mode" gorm:"not null;default:rwo"`
	// May include a CIFS password; never returned, and immutable after creation.
	DriverOptsEnc    string `json:"-" gorm:"type:text"`
	StorageClassName string `json:"storage_class" gorm:"not null;default:default"`
	// Not a secret: it is the bind source mounted directly into the app's containers.
	HostPath string `json:"host_path,omitempty"`
	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata    Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	Annotations Metadata  `json:"annotations,omitempty" gorm:"serializer:json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
