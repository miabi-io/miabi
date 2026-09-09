// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// DatabaseBackupSetRefPrefix marks a database recovery point, the way
// dr.RefPrefix marks a platform one.
const DatabaseBackupSetRefPrefix = "mbdb_"

// DatabaseBackupSet groups the per-database backups taken from one instance in a
// single run. Without it, backing up an instance's eight databases produces eight
// unrelated dumps taken at eight different instants: nothing to restore as a unit
// and nothing to name when someone asks for "last night".
//
// Modelled on PlatformBackupSet, including its central rule — an item failing
// fails the set, because a partial recovery point is not a recovery point.
type DatabaseBackupSet struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index;not null"`
	InstanceID  uint `json:"instance_id" gorm:"index;not null"`

	// Ref is the stable, human-quotable name of this recovery point:
	// "mbdb_<instance>_<UTC stamp>". Unique and never reused.
	Ref     string       `json:"ref" gorm:"uniqueIndex;not null"`
	Trigger string       `json:"trigger"` // manual | scheduled
	Status  BackupStatus `json:"status" gorm:"not null;default:pending"`

	// Engine and Version describe what the set was taken from. Recorded on the set
	// as well as its items because a restore preflight asks the question once.
	Engine  DBEngine `json:"engine"`
	Version string   `json:"version,omitempty"`
	// Encrypted reports that the artifacts are GPG-encrypted, so the passphrase is
	// needed to restore. False on a set with no items.
	Encrypted bool `json:"encrypted" gorm:"not null;default:false"`
	// Envelope seals this set's random data key under the workspace backup
	// passphrase (see internal/dbenvelope). The artifacts are encrypted with that
	// key rather than the passphrase itself, so rotating the passphrase re-seals a
	// few hundred bytes instead of stranding every set behind the secret it was
	// taken with. Never serialized: it is ciphertext, but publishing it would hand
	// out the thing an offline attack targets.
	Envelope string `json:"-" gorm:"type:text"`

	Destination string `json:"destination"` // local | s3
	S3Bucket    string `json:"s3_bucket,omitempty"`
	S3Path      string `json:"s3_path,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`

	// Error carries why the set did not complete — either its own failure or the
	// first item's.
	Error string `json:"error,omitempty" gorm:"type:text"`

	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`

	// Items are this recovery point's per-database backups. CASCADE because they
	// have no meaning without it: an artifact whose set is gone cannot be restored
	// as part of one, and leaving it behind shows half a recovery point in the
	// history.
	Items []Backup `json:"items,omitempty" gorm:"foreignKey:SetID;constraint:OnDelete:CASCADE"`
}

// NewDatabaseBackupSetRef builds a set's stable name from the instance it came
// from and the moment it started: "mbdb_<instance>_20260909T030000Z".
func NewDatabaseBackupSetRef(instance string, at time.Time) string {
	name := instance
	if name == "" {
		name = "unknown"
	}
	return DatabaseBackupSetRefPrefix + name + "_" + at.UTC().Format("20060102T150405Z")
}

// HasPinnedItem reports whether any member is pinned, which exempts the whole set
// from retention: pruning a set around a pinned member would leave exactly the
// half-recovery-point the set exists to prevent.
func (s *DatabaseBackupSet) HasPinnedItem() bool {
	for i := range s.Items {
		if s.Items[i].Pinned {
			return true
		}
	}
	return false
}

// DatabaseBackupSetSchedule runs a recovery point across an instance on a cron,
// then applies retention to what it produced.
//
// Deliberately separate from BackupSchedule rather than a nullable InstanceID on
// it: that model is keyed to a required DatabaseID, its retention counts
// individual backups rather than sets, and it still carries the per-schedule S3
// fields that are no longer used. Widening it would weaken a constraint every
// existing row depends on.
type DatabaseBackupSetSchedule struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index;not null"`
	InstanceID  uint   `json:"instance_id" gorm:"index;not null"`
	Cron        string `json:"cron" gorm:"not null"`
	Enabled     bool   `json:"enabled" gorm:"not null;default:true"`

	// Retention, applied after each run. 0 = unlimited. MaxSets counts recovery
	// points, not the backups inside them.
	MaxSets       int `json:"max_sets" gorm:"not null;default:0"`
	RetentionDays int `json:"retention_days" gorm:"not null;default:0"`

	// Concurrency caps how many databases are dumped at once, as for a manual run.
	// 0 uses the conservative service default.
	Concurrency int `json:"concurrency" gorm:"not null;default:0"`

	LastRunAt *time.Time `json:"last_run_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}
