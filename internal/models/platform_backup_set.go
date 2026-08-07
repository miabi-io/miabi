// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// PlatformBackupSet groups the artifacts of one platform backup run into a single
// recovery point: the sealed identity envelope, the control-plane database dump,
// and each platform volume archive. Disaster recovery consumes a *set*, never a
// loose artifact — restoring a database dump without the identity envelope that
// carries the encryption key produces a platform whose every secret is
// undecryptable, and restoring it without its volumes produces a control plane
// describing state that is not there.
//
// The provenance fields are read by the restore preflight before anything on the
// target host is touched, so a mismatch is refused rather than discovered hours
// later.
type PlatformBackupSet struct {
	ID uint `json:"id" gorm:"primaryKey"`

	// Ref is the stable, human-quotable name of this recovery point:
	// "mbdr_<install-id>_<UTC stamp>". It is what an operator passes to
	// `miabi restore --ref`, so it is unique and never reused.
	Ref     string       `json:"ref" gorm:"uniqueIndex;not null"`
	Trigger string       `json:"trigger"` // manual | scheduled
	Status  BackupStatus `json:"status" gorm:"not null;default:pending"`

	// Provenance — captured at set creation, checked by restore preflight.
	InstallID     string `json:"install_id"`
	MiabiVersion  string `json:"miabi_version"`
	SchemaVersion string `json:"schema_version"` // last applied upgrade step
	// KEKFingerprint proves two installs share a master encryption key without
	// revealing it: HMAC-SHA256(KEK, KEKFingerprintLabel) via crypto.DeriveToken.
	// Restore recomputes it after loading the identity envelope and refuses to
	// continue on a mismatch.
	KEKFingerprint string `json:"kek_fingerprint"`
	Encrypted      bool   `json:"encrypted"`       // artifacts are GPG-encrypted
	IdentitySealed bool   `json:"identity_sealed"` // an identity envelope is present

	Destination string `json:"destination"` // local | s3
	S3Bucket    string `json:"s3_bucket,omitempty"`
	S3Path      string `json:"s3_path,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`

	// Error carries why a set failed to complete (an item failing marks the set
	// failed: a partial recovery point is not a recovery point).
	Error string `json:"error,omitempty" gorm:"type:text"`

	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`

	// Items are the artifacts of this recovery point. CASCADE because they have no
	// meaning without it: an artifact whose set is gone cannot be restored, and
	// leaving it behind would show a half a recovery point in the history.
	Items []PlatformBackup `json:"items,omitempty" gorm:"foreignKey:SetID;constraint:OnDelete:CASCADE"`
}

// KEKFingerprintLabel is the fixed crypto.DeriveToken label for the master-key
// fingerprint recorded on a set. It is a constant because changing it would make
// every existing recovery point unverifiable.
const KEKFingerprintLabel = "dr:kek-fingerprint"

// RestorePendingKey is the platform setting written by `miabi restore` before the
// restored control plane's first boot. While it is true the platform runs
// quiesced — schedules do not fire and nothing auto-deploys — until an admin
// completes recovery. It exists so a freshly restored platform cannot race to
// reconcile against DNS that still points at the host it was recovered from.
const RestorePendingKey = "dr.restore_pending"

// Complete reports whether the set is a usable recovery point: it finished and
// carries a control-plane database dump. Volumes and the identity envelope are
// recorded separately — a set without an envelope can still be restored *onto
// the original host*, where the encryption key already exists.
func (s *PlatformBackupSet) Complete() bool {
	if s.Status != BackupCompleted {
		return false
	}
	for _, it := range s.Items {
		if it.Subject == PlatformBackupDatabase && it.Status == BackupCompleted && it.Filename != "" {
			return true
		}
	}
	return false
}

// Item returns the set's first item for a subject (and, for volumes, a name).
func (s *PlatformBackupSet) Item(subject PlatformBackupSubject, volume string) *PlatformBackup {
	for i := range s.Items {
		it := &s.Items[i]
		if it.Subject != subject {
			continue
		}
		if subject == PlatformBackupVolume && it.VolumeName != volume {
			continue
		}
		return it
	}
	return nil
}
