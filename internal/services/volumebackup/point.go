// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package volumebackup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dbenvelope"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
)

// PointInfoSchema is the descriptor's payload version. A reader refuses a schema it does not
// know rather than guessing at fields, as backup.SetInfoSchema does.
const PointInfoSchema = 1

// PointInfoKind tells a volume descriptor apart from a database set's in a shared bucket.
const PointInfoKind = "volume"

var (
	// ErrPassphraseRequired means the archive is sealed and the workspace has no passphrase
	// left to open it with.
	ErrPassphraseRequired = errors.New("this recovery point is encrypted and the workspace has no backup passphrase to open it")
	// ErrNotCompleted refuses to verify a backup that never produced an archive.
	ErrNotCompleted = errors.New("only a completed backup can be verified")
)

// PointInfo is the CLEARTEXT descriptor written beside a volume recovery point, so a bucket can
// be read without the passphrase or the platform that wrote it. It carries the shape of the
// data and never any of it; the envelope is ciphertext. See backup.SetInfo.
type PointInfo struct {
	Schema int    `json:"schema"`
	Kind   string `json:"kind"`

	Ref          string    `json:"ref"`
	Volume       string    `json:"volume"`
	StorageClass string    `json:"storage_class,omitempty"`
	ServerID     uint      `json:"server_id"`
	Consistency  string    `json:"consistency"`
	Trigger      string    `json:"trigger,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	SizeBytes    int64     `json:"size_bytes"`

	Encrypted bool   `json:"encrypted"`
	Envelope  string `json:"envelope,omitempty"`
	Filename  string `json:"filename"`
}

// PointPrefix is where a recovery point's objects live: one directory per point, under one
// per volume, beneath the workspace's volume backup path.
func PointPrefix(base, volume, ref string) string {
	return path.Join(strings.Trim(strings.TrimSpace(base), "/"), volume, ref)
}

func newSealedKey(passphrase string) (dataKey, sealed string, err error) {
	if dataKey, err = dbenvelope.NewDataKey(); err != nil {
		return "", "", err
	}
	if sealed, err = dbenvelope.Seal(dataKey, passphrase); err != nil {
		return "", "", fmt.Errorf("seal the recovery point data key: %w", err)
	}
	return dataKey, sealed, nil
}

func (s *Service) passphrase(workspaceID uint) (string, error) {
	if s.s3 == nil {
		return "", nil
	}
	return s.s3.BackupPassphrase(workspaceID)
}

// Sealing reports whether new recovery points in the workspace will be encrypted.
func (s *Service) Sealing(workspaceID uint) bool {
	pass, err := s.passphrase(workspaceID)
	return err == nil && pass != ""
}

// archiveKeyPassphrase returns what the helper needs to decrypt b's archive: the data key from
// its envelope, the workspace passphrase for a sealed archive with no envelope, or "" for a
// cleartext one. Keyed off the artifact, so an archive taken before a passphrase existed still
// restores without one.
func (s *Service) archiveKeyPassphrase(b *models.VolumeBackup) (string, error) {
	if !strings.HasSuffix(b.Filename, ".gpg") {
		return "", nil
	}
	pass, err := s.passphrase(b.WorkspaceID)
	if err != nil {
		return "", err
	}
	if pass == "" {
		return "", ErrPassphraseRequired
	}
	if b.Envelope == "" {
		return pass, nil
	}
	key, err := dbenvelope.Open(b.Envelope, pass)
	if err != nil {
		return "", fmt.Errorf("recovery point %s: %w", b.Ref, err)
	}
	return key, nil
}

// finishPoint runs what makes a completed archive a recovery point: the descriptor, an
// immediate verification, the owning schedule's retention and the alert resolution.
func (s *Service) finishPoint(ctx context.Context, vol *models.Volume, b *models.VolumeBackup, passphrase string) {
	// Best-effort: failing here would discard a point whose archive is already safely stored.
	if err := s.writeInfo(ctx, b, vol); err != nil {
		logger.Error("write volume recovery point descriptor; the point is stored but will not be discoverable",
			"ref", b.Ref, "error", err)
	}
	res, err := s.verifyWith(ctx, b, passphrase)
	if err != nil {
		logger.Warn("could not verify the new volume recovery point", "ref", b.Ref, "error", err)
	}
	if s.alerter != nil && res != nil && res.OK {
		s.alerter.VolumeBackupSucceeded(b.WorkspaceID, b.VolumeID)
	}
	if b.ScheduleID != nil {
		s.applyRetention(ctx, *b.ScheduleID, vol.ID)
	}
}

func (s *Service) applyRetention(ctx context.Context, scheduleID, volumeID uint) {
	sched, err := s.repo.FindScheduleByID(scheduleID)
	if err != nil {
		return
	}
	if sched.MaxPoints <= 0 && sched.RetentionDays <= 0 {
		return
	}
	if _, err := s.Prune(ctx, volumeID, sched.MaxPoints, sched.RetentionDays); err != nil {
		logger.Error("prune volume recovery points", "volume", volumeID, "error", err)
	}
}

func pointInfoFor(b *models.VolumeBackup, vol *models.Volume) PointInfo {
	info := PointInfo{
		Schema:      PointInfoSchema,
		Kind:        PointInfoKind,
		Ref:         b.Ref,
		Consistency: b.Consistency,
		Trigger:     b.Trigger,
		CreatedAt:   b.CreatedAt,
		SizeBytes:   b.SizeBytes,
		Encrypted:   b.Encrypted,
		Envelope:    b.Envelope,
		Filename:    b.Filename,
		ServerID:    b.ServerID,
	}
	if vol != nil {
		info.Volume = vol.Name
		info.StorageClass = vol.StorageClassName
	}
	return info
}

func (s *Service) writeInfo(ctx context.Context, b *models.VolumeBackup, vol *models.Volume) error {
	store, err := s.store(b.WorkspaceID)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(pointInfoFor(b, vol), "", "  ")
	if err != nil {
		return fmt.Errorf("encode point info: %w", err)
	}
	return store.Put(ctx, path.Join(strings.Trim(b.S3Path, "/"), backup.SetInfoObject), body)
}

// Verify re-checks a completed backup against the bucket: the archive is still there, still
// the size it was stored at, and a sealed point's envelope still opens with the workspace
// passphrase. It does not read the archive back — that costs the size of the backup every
// time, which is how verification becomes the thing people switch off.
func (s *Service) Verify(ctx context.Context, b *models.VolumeBackup) (*backup.VerifyResult, error) {
	if b.Status != models.BackupCompleted || b.Filename == "" {
		return nil, ErrNotCompleted
	}
	pass, err := s.passphrase(b.WorkspaceID)
	if err != nil {
		return nil, err
	}
	return s.verifyWith(ctx, b, pass)
}

func (s *Service) verifyWith(ctx context.Context, b *models.VolumeBackup, passphrase string) (*backup.VerifyResult, error) {
	store, err := s.store(b.WorkspaceID)
	if err != nil {
		return nil, err
	}
	key := archiveKey(b.S3Path, b.Filename)
	objs, err := store.List(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", key, err)
	}
	size, found := int64(0), false
	for _, o := range objs {
		if o.Key == key {
			size, found = o.Size, true
			break
		}
	}
	res := checkPoint(b, found, size, passphrase)
	s.recordVerification(b, res)
	return res, nil
}

// checkPoint is the judgement, separated from the bucket read so it is testable without one.
func checkPoint(b *models.VolumeBackup, found bool, size int64, passphrase string) *backup.VerifyResult {
	res := &backup.VerifyResult{Ref: b.Ref, Checked: 1}
	if res.Ref == "" {
		res.Ref = b.Filename
	}
	res.EnvelopeOK = b.Envelope == ""
	envelopeErr := ""
	if b.Envelope != "" {
		switch {
		case passphrase == "":
			envelopeErr = "encrypted, and no workspace backup passphrase is set to check it with"
		default:
			if _, err := dbenvelope.Open(b.Envelope, passphrase); err != nil {
				envelopeErr = "the workspace passphrase no longer opens this recovery point"
			} else {
				res.EnvelopeOK = true
			}
		}
	}
	switch {
	case !found:
		res.Missing = []string{b.Filename}
		res.Error = "the archive is missing from the bucket: " + b.Filename
	case b.SizeBytes > 0 && size != b.SizeBytes:
		res.Resized = []string{b.Filename}
		res.Error = fmt.Sprintf("the archive is %d bytes, not the %d it was stored at", size, b.SizeBytes)
	default:
		res.Error = envelopeErr
	}
	res.OK = res.Error == "" && res.EnvelopeOK
	return res
}

// recordVerification persists the outcome and raises a failure where someone will see it.
func (s *Service) recordVerification(b *models.VolumeBackup, res *backup.VerifyResult) {
	now := time.Now()
	b.VerifiedAt = &now
	b.VerifyError = res.Error
	b.VerifyStatus = backup.VerifyFailed
	if res.OK {
		b.VerifyStatus = backup.VerifyOK
	}
	if err := s.repo.Update(b); err != nil {
		logger.Error("record volume backup verification", "volume_backup", b.ID, "error", err)
	}
	if res.OK {
		return
	}
	logger.Warn("volume backup failed verification", "volume_backup", b.ID, "ref", b.Ref, "error", res.Error)
	if s.alerter != nil {
		s.alerter.VolumeBackupFailed(b.WorkspaceID, b.VolumeID, b.VolumeName, b.Ref, "verification: "+res.Error)
	}
}

// SetPinned pins or unpins a recovery point. Only points take part in retention, so pinning a
// plain archive is refused rather than silently meaning nothing.
func (s *Service) SetPinned(b *models.VolumeBackup, pinned bool) error {
	if !b.IsPoint() {
		return errors.New("only a recovery point can be pinned")
	}
	b.Pinned = pinned
	return s.repo.Update(b)
}

// Prune enforces retention over a volume's recovery points: keep at most maxPoints, and drop
// any older than retentionDays. Returns the number removed. Plain archives are never touched.
func (s *Service) Prune(ctx context.Context, volumeID uint, maxPoints, retentionDays int) (int, error) {
	if maxPoints <= 0 && retentionDays <= 0 {
		return 0, nil
	}
	points, err := s.repo.ListPointsByVolume(volumeID)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, i := range prunable(points, maxPoints, retentionDays, time.Now()) {
		if err := s.Delete(ctx, &points[i]); err != nil {
			logger.Error("prune volume recovery point", "ref", points[i].Ref, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		logger.Info("pruned volume recovery points", "volume", volumeID, "removed", removed)
	}
	return removed, nil
}

// prunable picks which points retention removes, given them newest first. The rules match
// backup.PruneSets: a pinned point is never pruned and does not occupy a slot; the newest
// completed point always survives, so a mistyped policy cannot leave a volume with nothing;
// and a run still in flight is left alone.
func prunable(points []models.VolumeBackup, maxPoints, retentionDays int, now time.Time) []int {
	keep := -1
	for i := range points {
		if points[i].Status == models.BackupCompleted {
			keep = i
			break
		}
	}
	var cutoff time.Time
	if retentionDays > 0 {
		cutoff = now.AddDate(0, 0, -retentionDays)
	}
	var out []int
	rank := 0
	for i := range points {
		p := &points[i]
		if p.Pinned {
			continue
		}
		overCount := maxPoints > 0 && rank >= maxPoints
		tooOld := retentionDays > 0 && p.CreatedAt.Before(cutoff)
		rank++
		if i == keep || p.Status == models.BackupPending || p.Status == models.BackupRunning {
			continue
		}
		if overCount || tooOld {
			out = append(out, i)
		}
	}
	return out
}

// RewrapSets re-seals every sealed volume point in a workspace from one passphrase to the next,
// and refreshes the descriptors that carry the envelope. Named to satisfy
// backupsettings.EnvelopeRotator alongside the database sets.
func (s *Service) RewrapSets(workspaceID uint, oldPassphrase, newPassphrase string) (int, error) {
	if oldPassphrase == "" || newPassphrase == "" || oldPassphrase == newPassphrase {
		return 0, nil
	}
	sealed, err := s.repo.ListSealed(workspaceID)
	if err != nil {
		return 0, err
	}
	rewrapped := 0
	for i := range sealed {
		b := &sealed[i]
		next, err := dbenvelope.Rewrap(b.Envelope, oldPassphrase, newPassphrase)
		if err != nil {
			return rewrapped, fmt.Errorf("volume recovery point %s: %w", b.Ref, err)
		}
		b.Envelope = next
		if err := s.repo.Update(b); err != nil {
			return rewrapped, fmt.Errorf("volume recovery point %s: %w", b.Ref, err)
		}
		rewrapped++
		// A stale descriptor would carry an envelope only the old passphrase opens.
		vol, _ := s.volumes.FindInWorkspace(b.WorkspaceID, b.VolumeID)
		if err := s.writeInfo(context.Background(), b, vol); err != nil {
			logger.Warn("refresh volume recovery point descriptor after rotation", "ref", b.Ref, "error", err)
		}
	}
	if rewrapped > 0 {
		logger.Info("rewrapped volume recovery point envelopes", "workspace", workspaceID, "points", rewrapped)
	}
	return rewrapped, nil
}

// SealedSetCount reports how many volume points in a workspace are sealed, so the passphrase
// that opens them cannot be discarded.
func (s *Service) SealedSetCount(workspaceID uint) (int, error) {
	sealed, err := s.repo.ListSealed(workspaceID)
	return len(sealed), err
}

// ListSchedules returns a volume's recovery-point schedules.
func (s *Service) ListSchedules(volumeID uint) ([]models.VolumeBackupSchedule, error) {
	return s.repo.ListSchedulesByVolume(volumeID)
}

// ListEnabledSchedules feeds the cron manager.
func (s *Service) ListEnabledSchedules() ([]models.VolumeBackupSchedule, error) {
	return s.repo.ListEnabledSchedules()
}

// RunSchedule takes one scheduled recovery point. Retention runs when the point lands, in
// whichever process runs it, because only then is there a completed point to keep.
func (s *Service) RunSchedule(ctx context.Context, workspaceID, scheduleID uint) error {
	sched, err := s.repo.FindScheduleInWorkspace(workspaceID, scheduleID)
	if err != nil {
		return fmt.Errorf("schedule not found: %w", err)
	}
	if !sched.Enabled {
		return nil
	}
	vol, err := s.volumes.FindInWorkspace(workspaceID, sched.VolumeID)
	if err != nil {
		return fmt.Errorf("volume not found: %w", err)
	}
	b, err := s.CreatePoint(ctx, vol, "scheduled", &sched.ID)
	now := time.Now()
	sched.LastRunAt = &now
	if uerr := s.repo.UpdateSchedule(sched); uerr != nil {
		logger.Error("record volume schedule run", "schedule", sched.ID, "error", uerr)
	}
	if err != nil {
		return err
	}
	if b.Status == models.BackupFailed {
		return fmt.Errorf("volume recovery point %s failed: %s", b.Ref, b.Error)
	}
	return nil
}
