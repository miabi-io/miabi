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
	"errors"
	"path"
	"strings"
	"time"
)

// Empty Path means "add no bind driver options": volumes live in Docker's own data root.
const DefaultStorageClassName = "default"

// Needed because a bind-backed Docker volume keeps its data after `docker volume rm`.
type ReclaimPolicy string

const (
	// ReclaimDelete removes the volume's directory under the class path.
	ReclaimDelete ReclaimPolicy = "delete"
	// ReclaimRetain keeps the directory; an admin reclaims it later.
	ReclaimRetain ReclaimPolicy = "retain"
)

// ValidReclaimPolicy reports whether p is a known reclaim policy.
func ValidReclaimPolicy(p ReclaimPolicy) bool { return p == ReclaimDelete || p == ReclaimRetain }

// ErrStorageClassName is returned for a class name that is not a short lowercase slug.
var ErrStorageClassName = errors.New("a storage class name is lowercase letters, digits and hyphens (max 32), e.g. ssd-fast")

// Names are immutable and land in manifests, so invalid input is refused rather than coerced.
func ValidStorageClassName(n string) bool { return poolNamePattern.MatchString(n) }

var ErrStorageClassPath = errors.New("storage class path must be a clean absolute path outside the system directories")

// The admin is the trust boundary, but these are never legitimate for tenant volumes and a typo is unrecoverable.
var forbiddenClassRoots = []string{
	"/", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root",
	"/run", "/sbin", "/sys", "/usr", "/var/lib/docker", "/var/run",
}

// An empty path is valid and means Docker's own data root.
func ValidateStorageClassPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if strings.ContainsRune(p, 0) || !strings.HasPrefix(p, "/") {
		return "", ErrStorageClassPath
	}
	clean := path.Clean(p)
	for _, bad := range forbiddenClassRoots {
		if clean == bad || (bad != "/" && strings.HasPrefix(clean, bad+"/")) {
			return "", ErrStorageClassPath
		}
	}
	return clean, nil
}

// StorageClass lets a tenant use operator storage by name without ever supplying a host path.
type StorageClass struct {
	UIDModel
	ID uint `json:"id" gorm:"primaryKey"`
	// Immutable: manifests in repos Miabi cannot rewrite reference it, so a rename would orphan or redirect them.
	Name        string `json:"name" gorm:"uniqueIndex;not null"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	// ServerID is the node the path exists on (0 = the local control-plane node).
	ServerID uint `json:"server_id" gorm:"index;not null;default:0"`
	// ServerName is the node's display name (transient; populated on read).
	ServerName string `json:"server_name,omitempty" gorm:"-"`
	ClusterID  uint   `json:"cluster_id" gorm:"index;not null;default:0"`
	// Immutable: re-pointing would not move data, it would aim live volumes at the wrong directory.
	Path string `json:"path"`
	// Operator's assertion that the path is the same filesystem on every node, which replicated services rely on.
	Shared bool `json:"shared" gorm:"not null;default:false"`
	// IsDefault marks the class used when a create names none. At most one per node.
	IsDefault bool `json:"is_default" gorm:"not null;default:false"`
	// Enabled off blocks new volumes; existing ones keep working.
	Enabled       bool          `json:"enabled" gorm:"not null;default:true"`
	ReclaimPolicy ReclaimPolicy `json:"reclaim_policy" gorm:"not null;default:delete"`
	Builtin       bool          `json:"builtin" gorm:"not null;default:false"`
	// 0 with a nil MeasuredAt means never measured.
	CapacityBytes  int64      `json:"capacity_bytes" gorm:"not null;default:0"`
	AvailableBytes int64      `json:"available_bytes" gorm:"not null;default:0"`
	MeasuredAt     *time.Time `json:"measured_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (c *StorageClass) Managed() bool { return c != nil && strings.TrimSpace(c.Path) != "" }

// Empty for the built-in class, which has no path of its own.
func (c *StorageClass) VolumePath(dockerName string) string {
	if !c.Managed() || strings.TrimSpace(dockerName) == "" {
		return ""
	}
	return path.Join(c.Path, dockerName)
}
