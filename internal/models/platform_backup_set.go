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

import "time"

// PlatformBackupSet is one recovery point; DR consumes a set, never a loose artifact.
type PlatformBackupSet struct {
	ID uint `json:"id" gorm:"primaryKey"`

	// Passed to `miabi restore --ref`, so unique and never reused.
	Ref     string       `json:"ref" gorm:"uniqueIndex;not null"`
	Trigger string       `json:"trigger"` // manual | scheduled
	Status  BackupStatus `json:"status" gorm:"not null;default:pending"`

	// Provenance — captured at set creation, checked by restore preflight.
	InstallID     string `json:"install_id"`
	MiabiVersion  string `json:"miabi_version"`
	SchemaVersion string `json:"schema_version"` // last applied upgrade step
	// HMAC of the KEK proving two installs share it without revealing it; restore refuses on mismatch.
	KEKFingerprint string `json:"kek_fingerprint"`
	Encrypted      bool   `json:"encrypted"`       // artifacts are GPG-encrypted
	IdentitySealed bool   `json:"identity_sealed"` // an identity envelope is present

	Destination string `json:"destination"` // local | s3
	S3Bucket    string `json:"s3_bucket,omitempty"`
	S3Path      string `json:"s3_path,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`

	// An item failing fails the set: a partial recovery point is not a recovery point.
	Error string `json:"error,omitempty" gorm:"type:text"`

	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`

	// CASCADE: items cannot be restored without their set, and leftovers would show half a recovery point.
	Items []PlatformBackup `json:"items,omitempty" gorm:"foreignKey:SetID;constraint:OnDelete:CASCADE"`
}

// Changing this label would make every existing recovery point unverifiable.
const KEKFingerprintLabel = "dr:kek-fingerprint"

// While true the platform runs quiesced so it cannot reconcile against DNS still pointing at the old host.
const RestorePendingKey = "dr.restore_pending"

// A set without an identity envelope can still be restored onto the original host.
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
