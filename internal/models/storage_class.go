// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"errors"
	"path"
	"strings"
	"time"
)

// DefaultStorageClassName is the seeded class every install has: it creates volumes the way Docker
// does, in the engine's own data root. Its Path is empty, which is the instruction "add no bind
// driver options".
const DefaultStorageClassName = "default"

// ReclaimPolicy decides what happens to a class-backed volume's directory when the volume is
// deleted. A bind-backed Docker volume keeps its data after `docker volume rm`, so the platform has
// to say explicitly whether it removes it.
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

// ValidStorageClassName reports whether n is a usable class name: a short lowercase slug. The name
// is immutable and ends up in manifests, so it is validated and refused rather than coerced — an
// admin who types "SSD Fast" should be told, not silently given "ssd-fast".
func ValidStorageClassName(n string) bool { return poolNamePattern.MatchString(n) }

// ErrStorageClassPath is returned for a class path that is not absolute, not clean, or names a
// system tree Miabi refuses to write volumes into.
var ErrStorageClassPath = errors.New("storage class path must be a clean absolute path outside the system directories")

// forbiddenClassRoots are trees a storage class may never be rooted at or inside. An admin choosing
// the path is the trust boundary, but these are never a legitimate place for tenant volumes and a
// typo here is unrecoverable.
var forbiddenClassRoots = []string{
	"/", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root",
	"/run", "/sbin", "/sys", "/usr", "/var/lib/docker", "/var/run",
}

// ValidateStorageClassPath cleans p and verifies it is an absolute path that is not a forbidden
// system tree, returning the cleaned path. An empty path is valid and means the built-in class:
// Docker's own data root.
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

// StorageClass is an admin-approved directory on a node where Miabi creates volumes. The tenant
// names a class; the platform derives the path — so an unprivileged workspace can use operator
// storage without ever supplying a host path (unlike VolumeDriverHost).
type StorageClass struct {
	UIDModel
	ID uint `json:"id" gorm:"primaryKey"`
	// Name is the handle the API, the CLI and manifests use. IMMUTABLE: volumes and GitOps
	// manifests reference a class by name, and manifests live in repositories Miabi cannot
	// rewrite, so a rename would orphan or silently redirect them.
	Name        string `json:"name" gorm:"uniqueIndex;not null"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	// ServerID is the node the path exists on (0 = the local control-plane node).
	ServerID uint `json:"server_id" gorm:"index;not null;default:0"`
	// ServerName is the node's display name (transient; populated on read).
	ServerName string `json:"server_name,omitempty" gorm:"-"`
	ClusterID  uint   `json:"cluster_id" gorm:"index;not null;default:0"`
	// Path is the Miabi-owned directory volumes are created under, as <Path>/<DockerName>. Empty
	// means Docker's own data root (the built-in class). IMMUTABLE: volumes already exist under it,
	// so re-pointing it would not move data — it would point live volumes at the wrong directory.
	Path string `json:"path"`
	// Shared records the operator's assertion that this path is the same filesystem on every node
	// of the cluster, which is what allows a replicated service to rely on it.
	Shared bool `json:"shared" gorm:"not null;default:false"`
	// IsDefault marks the class used when a create names none. At most one per node.
	IsDefault bool `json:"is_default" gorm:"not null;default:false"`
	// Enabled off blocks new volumes; existing ones keep working.
	Enabled       bool          `json:"enabled" gorm:"not null;default:true"`
	ReclaimPolicy ReclaimPolicy `json:"reclaim_policy" gorm:"not null;default:delete"`
	// Builtin marks the seeded "default" class: it cannot be deleted, and its key and path are
	// fixed even by the rules that already make those immutable.
	Builtin bool `json:"builtin" gorm:"not null;default:false"`
	// CapacityBytes and AvailableBytes are the filesystem's measured size, refreshed by the storage
	// sweep. 0 with a nil MeasuredAt means never measured.
	CapacityBytes  int64      `json:"capacity_bytes" gorm:"not null;default:0"`
	AvailableBytes int64      `json:"available_bytes" gorm:"not null;default:0"`
	MeasuredAt     *time.Time `json:"measured_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Managed reports whether the class puts volumes on an operator-chosen path rather than leaving
// them to Docker's data root.
func (c *StorageClass) Managed() bool { return c != nil && strings.TrimSpace(c.Path) != "" }

// VolumePath is the directory a volume with the given Docker name occupies under this class. Empty
// for the built-in class, which has no path of its own.
func (c *StorageClass) VolumePath(dockerName string) string {
	if !c.Managed() || strings.TrimSpace(dockerName) == "" {
		return ""
	}
	return path.Join(c.Path, dockerName)
}
