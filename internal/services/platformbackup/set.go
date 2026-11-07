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
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/blob"
	"gorm.io/gorm"
)

// Wired at the composition root because these values come from process config and platform settings.
type IdentitySource func() (*dr.Identity, error)

// SetIdentitySource is optional; without it recovery points restore onto this host only, not a fresh one.
func (s *Service) SetIdentitySource(fn IdentitySource) { s.identity = fn }

// SetFingerprinter is for tests; production uses crypto.DeriveToken over KEKFingerprintLabel.
func (s *Service) SetFingerprinter(fn func(label string) string) { s.fingerprint = fn }

// ListSets returns recovery points, newest first.
func (s *Service) ListSets() ([]models.PlatformBackupSet, error) { return s.sets.List() }

// ListSetsPaged returns a page of recovery points plus the total count.
func (s *Service) ListSetsPaged(limit, offset int) ([]models.PlatformBackupSet, int64, error) {
	return s.sets.ListPaged(limit, offset)
}

// GetSet returns one recovery point with its items.
func (s *Service) GetSet(id uint) (*models.PlatformBackupSet, error) { return s.sets.FindByID(id) }

// GetSetByRef resolves a recovery point by the ref an operator quotes.
func (s *Service) GetSetByRef(ref string) (*models.PlatformBackupSet, error) {
	return s.sets.FindByRef(ref)
}

// CreateSet requires S3: a recovery point stored on the host it protects is unreadable once that host is gone.
func (s *Service) CreateSet(ctx context.Context, trigger string) (*models.PlatformBackupSet, error) {
	// Detached: the handler returns once the set is recorded, and the envelope or inline artifacts would be cancelled.
	ctx = context.WithoutCancel(ctx)

	st, err := s.getSettings()
	if err != nil {
		return nil, err
	}
	cfg, err := s.s3Config(st)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, ErrSetNeedsS3
	}
	// Encryption without a passphrase degrades rather than blocks: an unencrypted recovery point beats none.
	encrypt := st.EncryptBackups
	if encrypt && st.BackupPassphraseEnc == "" {
		logger.Warn("no backup passphrase is set: this recovery point will be written UNENCRYPTED. Set MIABI_PLATFORM_BACKUP_PASSPHRASE, or turn encryption off to stop seeing this")
		encrypt = false
	}

	started := time.Now()
	installID := ""
	var identity *dr.Identity
	if s.identity != nil {
		if identity, err = s.identity(); err != nil {
			return nil, fmt.Errorf("resolve platform identity: %w", err)
		}
		installID = identity.InstallID
	}

	// Derived from the key in the envelope, so a restore never refuses a good point if the two sources diverged.
	fingerprint := s.kekFingerprint()
	if identity != nil {
		if fp := s.fingerprintOf(identity.EncryptionKey); fp != "" {
			fingerprint = fp
		}
	}

	set := &models.PlatformBackupSet{
		Ref:            dr.NewRef(installID, started),
		Trigger:        trigger,
		Status:         models.BackupPending,
		InstallID:      installID,
		KEKFingerprint: fingerprint,
		Encrypted:      encrypt,
		Destination:    destS3,
		S3Bucket:       cfg.Bucket,
		S3Path:         st.DatabaseBackupPath,
		StartedAt:      &started,
	}
	if identity != nil {
		set.MiabiVersion = identity.MiabiVersion
		set.SchemaVersion = identity.DBSchema
		set.IdentitySealed = st.IncludeIdentity && st.BackupPassphraseEnc != ""
	}
	if err := s.sets.Create(set); err != nil {
		return nil, err
	}

	// Sealed inline so a failed envelope fails the point now instead of at restore time.
	if set.IdentitySealed {
		item := &models.PlatformBackup{
			SetID: &set.ID, Subject: models.PlatformBackupIdentity,
			Status: models.BackupPending, Trigger: trigger, Destination: "s3",
			S3Bucket: cfg.Bucket, S3Path: st.DatabaseBackupPath, Encrypted: true,
		}
		if err := s.repo.Create(item); err != nil {
			return nil, s.failSet(set, err)
		}
		if err := s.runIdentityBackup(ctx, item, st); err != nil {
			return nil, s.failSet(set, err)
		}
		if item.Status != models.BackupCompleted {
			return nil, s.failSet(set, errors.New(item.Error))
		}
	}

	queued := 0
	if st.IncludeTenantData {
		// Tenant data runs inline before the dump is enqueued, so a half-captured tenant set fails the point.
		queued += s.captureTenantData(ctx, set, st, cfg, trigger)
	}
	if err := s.enqueueItem(ctx, set, st, cfg.Bucket, models.PlatformBackupDatabase, "", trigger); err != nil {
		return nil, s.failSet(set, err)
	}
	queued++
	for _, vol := range st.Volumes {
		if err := s.assertBackupable(ctx, vol); err != nil {
			logger.Warn("skipping excluded volume in recovery point", "volume", vol, "error", err)
			continue
		}
		if err := s.enqueueItem(ctx, set, st, cfg.Bucket, models.PlatformBackupVolume, vol, trigger); err != nil {
			return nil, s.failSet(set, err)
		}
		queued++
	}

	set.Status = models.BackupRunning
	if err := s.sets.Update(set); err != nil {
		return nil, err
	}
	if !set.IdentitySealed {
		// Without an envelope the point restores only where the original MIABI_ENCRYPTION_KEY exists.
		logger.Warn("recovery point has no identity envelope: it can be restored onto this platform's own host, but not onto a fresh one. Set a backup passphrase to seal the encryption key with it",
			"ref", set.Ref)
	}
	logger.Info("platform recovery point started", "ref", set.Ref, "items", queued,
		"encrypted", set.Encrypted, "identity", set.IdentitySealed)
	// Without a worker the items already ran synchronously, so the set is done.
	s.finalizeSet(&set.ID)
	return s.sets.FindByID(set.ID)
}

func (s *Service) enqueueItem(ctx context.Context, set *models.PlatformBackupSet, st *models.PlatformBackupSettings, bucket string, subject models.PlatformBackupSubject, volume, trigger string) error {
	path := st.DatabaseBackupPath
	if subject == models.PlatformBackupVolume {
		path = st.VolumeBackupPath
	}
	item := &models.PlatformBackup{
		SetID: &set.ID, Subject: subject, VolumeName: volume,
		Status: models.BackupPending, Trigger: trigger, Destination: destS3,
		S3Bucket: bucket, S3Path: path,
	}
	if err := s.repo.Create(item); err != nil {
		return err
	}
	if s.enqueuer == nil {
		return s.RunBackup(ctx, item.ID)
	}
	if err := s.enqueuer.EnqueuePlatformBackup(item.ID); err != nil {
		s.fail(item, fmt.Errorf("enqueue backup: %w", err))
		return err
	}
	return nil
}

// In-process because the payload is the master key; a container env would expose it to `docker inspect`.
func (s *Service) runIdentityBackup(ctx context.Context, b *models.PlatformBackup, st *models.PlatformBackupSettings) error {
	now := time.Now()
	b.Status = models.BackupRunning
	b.StartedAt = &now
	_ = s.repo.Update(b)

	if s.identity == nil {
		s.fail(b, errors.New("no platform identity source is wired"))
		return nil
	}
	pass, err := s.passphrase(st)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	if pass == "" {
		s.fail(b, ErrNoPassphrase)
		return nil
	}
	identity, err := s.identity()
	if err != nil {
		s.fail(b, fmt.Errorf("resolve platform identity: %w", err))
		return nil
	}
	sealed, err := dr.Seal(identity, pass)
	if err != nil {
		s.fail(b, fmt.Errorf("seal identity envelope: %w", err))
		return nil
	}
	store, err := s.blobStore(st)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	set, err := s.sets.FindByID(*b.SetID)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	key := dr.IdentityObject(st.RootPath, set.Ref)
	if err := store.Put(ctx, key, sealed); err != nil {
		s.fail(b, err)
		return nil
	}

	fin := time.Now()
	b.Filename = key
	b.SizeBytes = int64(len(sealed))
	b.Status = models.BackupCompleted
	b.FinishedAt = &fin
	// Deliberately no log body: everything interesting about this run is secret.
	b.Logs = "identity envelope sealed and uploaded"
	if err := s.repo.Update(b); err != nil {
		return err
	}
	logger.Info("platform identity envelope sealed", "set", set.Ref, "object", key)
	return nil
}

func (s *Service) blobStore(st *models.PlatformBackupSettings) (*blob.Store, error) {
	cfg, err := s.s3Config(st)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, ErrS3NotConfigured
	}
	return blob.New(blob.Config{
		Endpoint:       cfg.Endpoint,
		Bucket:         cfg.Bucket,
		Region:         cfg.Region,
		AccessKey:      cfg.AccessKey,
		SecretKey:      cfg.SecretKey,
		UseSSL:         cfg.UseSSL,
		ForcePathStyle: cfg.ForcePathStyle,
	})
}

// Restore refuses on mismatch rather than producing a platform full of undecryptable secrets.
func (s *Service) kekFingerprint() string {
	if s.fingerprint != nil {
		return s.fingerprint(models.KEKFingerprintLabel)
	}
	return ""
}

// finalizeSet completes a set only when every item completed: a partial recovery point is not one.
func (s *Service) finalizeSet(setID *uint) {
	if setID == nil || *setID == 0 {
		return
	}
	set, err := s.sets.FindByID(*setID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Error("finalize recovery point: load set", "set", *setID, "error", err)
		}
		return
	}
	if set.Status == models.BackupCompleted || set.Status == models.BackupFailed {
		return
	}
	// Pending means CreateSet is still adding items; finalizing now would judge the set on a partial list.
	if set.Status == models.BackupPending {
		return
	}

	var size int64
	failed := make([]string, 0, 2)
	for _, it := range set.Items {
		switch it.Status {
		case models.BackupPending, models.BackupRunning:
			return // still working
		case models.BackupFailed:
			// The set error is often all an operator sees, so carry each item's own error, not just its name.
			failed = append(failed, itemFailure(&it))
		}
		size += it.SizeBytes
	}

	fin := time.Now()
	set.FinishedAt = &fin
	set.SizeBytes = size
	if len(failed) > 0 {
		set.Status = models.BackupFailed
		set.Error = fmt.Sprintf("incomplete recovery point — %d of %d artifacts failed:\n%s",
			len(failed), len(set.Items), strings.Join(failed, "\n"))
		logger.Error("platform recovery point failed", "ref", set.Ref, "failed", len(failed), "detail", set.Error)
	} else {
		set.Status = models.BackupCompleted
		// Without the manifest a restore cannot learn what the point contains; the only other record is the lost DB.
		if err := s.publishManifest(context.Background(), set); err != nil {
			set.Status = models.BackupFailed
			set.Error = "could not publish the recovery point manifest: " + err.Error()
			logger.Error("publish recovery point manifest", "ref", set.Ref, "error", err)
		} else {
			logger.Info("platform recovery point completed", "ref", set.Ref, "items", len(set.Items), "bytes", size)
		}
	}
	if err := s.sets.Update(set); err != nil {
		logger.Error("finalize recovery point: save set", "set", *setID, "error", err)
	}
}

// So `miabi restore` can plan a recovery with only the bucket and the passphrase.
func (s *Service) publishManifest(ctx context.Context, set *models.PlatformBackupSet) error {
	st, err := s.getSettings()
	if err != nil {
		return err
	}
	store, err := s.blobStore(st)
	if err != nil {
		return err
	}
	man := &dr.Manifest{
		// Set explicitly: Validate runs before EncodeManifest fills it, and schema 0 fails validation.
		Schema:         dr.ManifestSchema,
		Ref:            set.Ref,
		InstallID:      set.InstallID,
		MiabiVersion:   set.MiabiVersion,
		SchemaVersion:  set.SchemaVersion,
		KEKFingerprint: set.KEKFingerprint,
		Encrypted:      set.Encrypted,
		IdentitySealed: set.IdentitySealed,
		Bucket:         set.S3Bucket,
		Prefix:         st.DatabaseBackupPath,
		VolumePrefix:   st.VolumeBackupPath,
		CreatedAt:      set.CreatedAt,
	}
	for _, it := range set.Items {
		if it.Status != models.BackupCompleted || it.Filename == "" {
			continue
		}
		man.Artifacts = append(man.Artifacts, dr.Artifact{
			Subject: string(it.Subject),
			// The actual write path; tenant artifacts live under a per-workspace branch that cannot be derived later.
			Path:      it.S3Path,
			Volume:    it.VolumeName,
			Workspace: it.WorkspaceSlug,
			Database:  it.DatabaseName,
			Engine:    it.Engine,
			File:      it.Filename,
			SizeBytes: it.SizeBytes,
			Encrypted: it.Encrypted,
		})
	}
	if err := man.Validate(); err != nil {
		return err
	}
	body, err := dr.EncodeManifest(man)
	if err != nil {
		return err
	}
	return store.Put(ctx, dr.ManifestObject(st.RootPath, set.Ref), body)
}

func (s *Service) failSet(set *models.PlatformBackupSet, cause error) error {
	fin := time.Now()
	set.Status = models.BackupFailed
	set.FinishedAt = &fin
	if cause != nil {
		set.Error = cause.Error()
	}
	if err := s.sets.Update(set); err != nil {
		logger.Error("mark recovery point failed", "set", set.ID, "error", err)
	}
	logger.Error("platform recovery point failed", "ref", set.Ref, "error", cause)
	return cause
}

// Retention works on whole sets: a dump pruned while its volumes remain recovers nothing.
func (s *Service) PruneSets(ctx context.Context, maxSets, retentionDays int) (int, error) {
	if maxSets <= 0 && retentionDays <= 0 {
		return 0, nil
	}
	sets, err := s.sets.ListCompletedOldestFirst()
	if err != nil {
		return 0, err
	}
	var cutoff time.Time
	if retentionDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -retentionDays)
	}
	overCount := 0
	if maxSets > 0 && len(sets) > maxSets {
		overCount = len(sets) - maxSets
	}

	removed := 0
	for i := range sets {
		set := &sets[i]
		tooMany := i < overCount
		tooOld := retentionDays > 0 && set.CreatedAt.Before(cutoff)
		if !tooMany && !tooOld {
			continue
		}
		if err := s.DeleteSet(ctx, set); err != nil {
			logger.Error("prune recovery point", "ref", set.Ref, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		logger.Info("pruned platform recovery points", "removed", removed)
	}
	return removed, nil
}

func (s *Service) DeleteSet(ctx context.Context, set *models.PlatformBackupSet) error {
	st, err := s.getSettings()
	if err != nil {
		return err
	}
	store, storeErr := s.blobStore(st)
	if storeErr == nil {
		// Manifest first: without it nothing looks restorable, so a partial delete is safe.
		if err := store.Delete(ctx, dr.ManifestObject(st.RootPath, set.Ref)); err != nil {
			logger.Error("delete recovery point manifest", "ref", set.Ref, "error", err)
		}
	}
	for i := range set.Items {
		item := &set.Items[i]
		if item.Subject == models.PlatformBackupIdentity && storeErr == nil {
			if err := store.Delete(ctx, item.Filename); err != nil {
				logger.Error("delete identity envelope", "object", item.Filename, "error", err)
			}
		}
		if err := s.Delete(ctx, item); err != nil {
			logger.Error("delete recovery point item", "item", item.ID, "error", err)
		}
	}
	return s.sets.Delete(set.ID)
}

// itemFailure describes one failed artifact: what it was, and why it failed.
func itemFailure(it *models.PlatformBackup) string {
	label := string(it.Subject)
	switch {
	case it.VolumeName != "":
		label += " " + it.VolumeName
	case it.DatabaseName != "":
		label += " " + it.WorkspaceSlug + "/" + it.DatabaseName
	}
	reason := strings.TrimSpace(it.Error)
	if reason == "" {
		reason = "failed with no recorded reason"
	}
	// A summary, not a transcript: full helper output stays on the item and in the log store.
	if len(reason) > 500 {
		reason = reason[:500] + "…"
	}
	return "  · " + label + ": " + reason
}
