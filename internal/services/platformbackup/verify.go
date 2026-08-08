// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// Restorable is the bottom line: this set can rebuild the platform on a fresh
	// host. False does not always mean useless — see Findings.
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
	// Severity is "error" (this set cannot restore) or "warning" (it can, with a
	// caveat the operator should know before they need it).
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func (r *VerifyReport) add(severity, format string, args ...any) {
	r.Findings = append(r.Findings, VerifyFinding{Severity: severity, Message: fmt.Sprintf(format, args...)})
}

// VerifySet checks a recovery point without restoring anything: every artifact is present in object
// storage, and the identity envelope opens with the passphrase and carries the master key this platform
// runs on. A recovery point that has never been opened is a hypothesis.
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

	// Every artifact the set claims must actually be in the bucket. A row saying
	// "completed" is a record of what happened months ago, not of what is there.
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

	// Encryption coverage is not uniform, and an operator who set a passphrase has
	// every reason to assume it is. Say which artifacts it did not reach.
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

// verifyIdentity opens the sealed envelope and checks it belongs to this
// recovery point. Every failure below is recorded rather than returned: the
// operator wants the whole verdict, not the first thing that went wrong.
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

	// Opening the envelope is not enough: it must carry the key this platform's
	// data is actually encrypted under. A stale envelope from before a key
	// rotation opens perfectly and restores nothing readable.
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

// fingerprintOf computes the fingerprint a given master key would produce, so a recovered envelope can be
// checked against the set that recorded it. It mirrors crypto.DeriveToken but takes the key explicitly,
// because the key under test is not this process's key.
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

// objectKey is the artifact's key within the bucket. The identity envelope
// records its full key; the *-bkup tools record a bare filename under the
// configured remote path.
func objectKey(b *models.PlatformBackup) string {
	if b.Subject == models.PlatformBackupIdentity {
		return b.Filename
	}
	if p := strings.Trim(b.S3Path, "/"); p != "" {
		return p + "/" + b.Filename
	}
	return b.Filename
}
