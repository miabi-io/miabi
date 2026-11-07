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

package platformbackup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/blob"
)

// VerifyReport is the outcome of a recovery-point drill.
type VerifyReport struct {
	Ref string `json:"ref"`
	// False does not always mean useless; see Findings.
	Restorable bool            `json:"restorable"`
	Findings   []VerifyFinding `json:"findings"`

	IdentityOpened bool   `json:"identity_opened"`
	KEKMatches     bool   `json:"kek_matches"`
	InstallID      string `json:"install_id,omitempty"`
	MiabiVersion   string `json:"miabi_version,omitempty"`
	ArtifactsFound int    `json:"artifacts_found"`
	ArtifactsTotal int    `json:"artifacts_total"`
}

// VerifyFinding is one problem, in the operator's terms.
type VerifyFinding struct {
	// "error" means the set cannot restore; "warning" means it can, with a caveat.
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func (r *VerifyReport) add(severity, format string, args ...any) {
	r.Findings = append(r.Findings, VerifyFinding{Severity: severity, Message: fmt.Sprintf(format, args...)})
}

// VerifySet checks artifact presence and that the envelope opens with this platform's master key, without restoring.
func (s *Service) VerifySet(ctx context.Context, set *models.PlatformBackupSet, passphrase string) (*VerifyReport, error) {
	st, err := s.getSettings()
	if err != nil {
		return nil, err
	}
	rep := &VerifyReport{Ref: set.Ref, Findings: []VerifyFinding{}}

	if set.Status != models.BackupCompleted {
		rep.add("error", "this recovery point did not complete (status %s)", set.Status)
	}

	store, err := s.blobStore(st)
	if err != nil {
		rep.add("error", "cannot reach the backup target: %v", err)
		return rep, nil
	}

	// A "completed" row records the past, not what the bucket holds now.
	var identityItem *models.PlatformBackup
	var hasDB bool
	for i := range set.Items {
		item := &set.Items[i]
		if item.Subject == models.PlatformBackupIdentity {
			identityItem = item
		}
		if item.Status != models.BackupCompleted || item.Filename == "" {
			rep.add("error", "artifact %s did not complete", artifactLabel(item))
			continue
		}
		rep.ArtifactsTotal++
		key := objectKey(item)
		found, err := store.Exists(ctx, key)
		if err != nil {
			rep.add("error", "could not check %s: %v", key, err)
			continue
		}
		if !found {
			rep.add("error", "%s is missing from the bucket (expected %s)", artifactLabel(item), key)
			continue
		}
		rep.ArtifactsFound++
		if item.Subject == models.PlatformBackupDatabase {
			hasDB = true
		}
	}
	if !hasDB {
		rep.add("error", "no control-plane database dump in this recovery point")
	}

	// The identity envelope is what makes this restorable onto a *fresh* host.
	if identityItem == nil {
		rep.add("warning", "no identity envelope: this set can be restored onto a host that still has the original MIABI_ENCRYPTION_KEY, but not onto a fresh one")
	} else {
		s.verifyIdentity(ctx, store, st, set, identityItem, passphrase, rep)
	}

	// Encryption coverage is not uniform, so name the artifacts the passphrase did not reach.
	if set.Encrypted {
		var plain []string
		for i := range set.Items {
			it := &set.Items[i]
			if it.Status == models.BackupCompleted && !it.Encrypted {
				plain = append(plain, artifactLabel(it))
			}
		}
		if len(plain) > 0 {
			rep.add("warning",
				"these artifacts are NOT encrypted because the volume backup tool has no encryption support — "+
					"they sit in the bucket in the clear: %s", strings.Join(plain, ", "))
		}
	}

	rep.Restorable = rep.ArtifactsFound == rep.ArtifactsTotal && hasDB && !hasError(rep)
	return rep, nil
}

// Failures are recorded, not returned: the operator wants the whole verdict.
func (s *Service) verifyIdentity(ctx context.Context, store *blob.Store, st *models.PlatformBackupSettings, set *models.PlatformBackupSet, item *models.PlatformBackup, passphrase string, rep *VerifyReport) {
	pass := passphrase
	if pass == "" {
		stored, err := s.passphrase(st)
		if err != nil {
			rep.add("error", "could not read the stored backup passphrase: %v", err)
			return
		}
		pass = stored
	}
	if pass == "" {
		rep.add("error", "no passphrase supplied and none stored: the identity envelope cannot be opened")
		return
	}

	sealed, err := store.GetBytes(ctx, objectKey(item))
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			rep.add("error", "the identity envelope is missing from the bucket")
		} else {
			rep.add("error", "could not read the identity envelope: %v", err)
		}
		return
	}
	identity, err := dr.Open(sealed, pass)
	if err != nil {
		rep.add("error", "the identity envelope did not open: %v", err)
		return
	}
	rep.IdentityOpened = true
	rep.InstallID = identity.InstallID
	rep.MiabiVersion = identity.MiabiVersion

	// A pre-rotation envelope opens fine but restores nothing readable, so the key fingerprint must match.
	if fp := s.fingerprintOf(identity.EncryptionKey); fp != "" && set.KEKFingerprint != "" {
		rep.KEKMatches = fp == set.KEKFingerprint
		if !rep.KEKMatches {
			rep.add("error", "the encryption key in the identity envelope does not match this recovery point's fingerprint — a restore from it would leave every secret undecryptable")
		}
	}
	if identity.RegistryStorage == models.RegistryStorageFilesystem {
		rep.add("warning", "the registry uses local storage, so images pushed to it are not in this recovery point; apps with no Git source will not come back after a host loss")
	}
}

// Takes the key explicitly because the key under test is not this process's key.
func (s *Service) fingerprintOf(key string) string {
	if s.keyFingerprint == nil {
		return ""
	}
	return s.keyFingerprint(key, models.KEKFingerprintLabel)
}

// SetKeyFingerprinter wires the fingerprint-of-an-arbitrary-key function.
func (s *Service) SetKeyFingerprinter(fn func(key, label string) string) { s.keyFingerprint = fn }

func hasError(r *VerifyReport) bool {
	for _, f := range r.Findings {
		if f.Severity == "error" {
			return true
		}
	}
	return false
}

func artifactLabel(b *models.PlatformBackup) string {
	if b.VolumeName != "" {
		return string(b.Subject) + " " + b.VolumeName
	}
	return string(b.Subject)
}

// The envelope records its full key; the *-bkup tools record a bare filename under the remote path.
func objectKey(b *models.PlatformBackup) string {
	if b.Subject == models.PlatformBackupIdentity {
		return b.Filename
	}
	if p := strings.Trim(b.S3Path, "/"); p != "" {
		return p + "/" + b.Filename
	}
	return b.Filename
}
