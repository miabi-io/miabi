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

// Package volumebackup archives a managed volume's contents to the workspace's S3 target using the ecosystem
// volume-bkup tool as a one-shot container, and restores it. Runs synchronously, mirroring the database
// backup service. S3 is required — there is no local volume-backup destination.
package volumebackup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/storage/blob"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	// ErrS3NotConfigured is returned when the workspace has no S3 backup target.
	ErrS3NotConfigured = errors.New("workspace S3 backup settings are not configured")
	// ErrNoArchive is returned when restoring a backup that has no archive.
	ErrNoArchive = errors.New("volume backup has no archive to restore")

	archiveRe = backup.VolumeArtifactRe
)

const (
	volumeMount      = "/data"
	defaultBkupImage = platformimage.DefaultVolumeBkup
)

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// S3Provider yields a workspace's S3 target plus the volume backup path prefix, and the
// passphrase recovery points are sealed under. Satisfied by backupsettings.Service.
type S3Provider interface {
	VolumeBackupTarget(workspaceID uint) (*backup.S3Config, string, error)
	BackupPassphrase(workspaceID uint) (string, error)
	// BackupAge returns the age recipients new points are encrypted to ("" for GPG) and the
	// stored identity that reads age archives back ("" when none is stored).
	BackupAge(workspaceID uint) (recipients, identity string, err error)
	// VerifyAfterBackup reports whether each archive is read back right after it is written.
	VerifyAfterBackup(workspaceID uint) bool
}

// Alerter raises and resolves the backup_failed alert for a volume's recovery points.
type Alerter interface {
	VolumeBackupFailed(workspaceID, volumeID uint, volumeName, ref, errMsg string)
	VolumeBackupSucceeded(workspaceID, volumeID uint)
}

// Enqueuer schedules a volume backup to run in the background worker. Satisfied
// by worker.Producer.
type Enqueuer interface {
	EnqueueVolumeBackup(backupID, serverID uint) error
}

type Service struct {
	repo     *repositories.VolumeBackupRepository
	volumes  *repositories.VolumeRepository
	clients  NodeDocker
	images   ImageResolver
	s3       S3Provider
	enqueuer Enqueuer
	alerter  Alerter
	logs     *logstore.Store
	// networks the helper is attached to so it can reach the object store from inside Docker.
	network         string
	internalNetwork string
}

// NewService builds the volume backup service. network is the shared proxy network: it is a
// constructor argument, not a setter, because the helper uploads for itself and an install whose
// object store is only reachable inside Docker fails without it — a new call site must decide.
func NewService(repo *repositories.VolumeBackupRepository, volumes *repositories.VolumeRepository, clients NodeDocker, network string) *Service {
	return &Service{repo: repo, volumes: volumes, clients: clients, network: network}
}

// SetImageResolver wires the deployment-config resolver for the volume-bkup image.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

// SetLogStore wires the shared execution-log store. When set, a volume-backup
// run's full output is externalized to the store on terminal state and the DB
// row keeps only a bounded tail + a reference. nil keeps DB-tail-only.
func (s *Service) SetLogStore(store *logstore.Store) { s.logs = store }

// externalizeLog moves a terminal volume-backup's full output into the shared log store and trims the row
// to a bounded tail + a reference. No-op when the store is disabled or already externalized; on any error
// the full log stays in the DB tail.
func (s *Service) externalizeLog(b *models.VolumeBackup) {
	if !s.logs.Enabled() || b.LogRef != "" {
		return
	}
	ref := logstore.VolumeBackupRef(b.WorkspaceID, b.ID)
	res, err := s.logs.Externalize(ref, b.Logs)
	if err != nil {
		logger.Error("log store: externalize volume backup log failed", "volume_backup", b.ID, "error", err)
		return
	}
	if err := s.repo.SetLogMeta(b.ID, res.Ref, res.Tail, res.Bytes, res.Lines, res.Truncated); err != nil {
		logger.Error("log store: record volume backup log ref failed", "volume_backup", b.ID, "error", err)
		return
	}
	b.LogRef, b.Logs = res.Ref, res.Tail
	b.LogBytes, b.LogLines, b.LogTruncated = res.Bytes, res.Lines, res.Truncated
}

// SetS3Provider wires the workspace S3 settings provider.
func (s *Service) SetS3Provider(p S3Provider) { s.s3 = p }

// SetInternalNetwork names the platform's private network, where a self-hosted object store lives on
// a split install. The helper is attached to it as well as the proxy network.
func (s *Service) SetInternalNetwork(name string) { s.internalNetwork = name }

// SetAlerter wires failure alerts for recovery points (nil-safe).
func (s *Service) SetAlerter(a Alerter) { s.alerter = a }

// SetEnqueuer wires the background worker producer. When unset, Create runs the
// backup synchronously (used in tests / no-redis setups).
func (s *Service) SetEnqueuer(e Enqueuer) { s.enqueuer = e }

func (s *Service) image() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyBackupVolume); r != "" {
			return r
		}
	}
	return defaultBkupImage
}

// List returns a volume's backup history (most recent first).
func (s *Service) List(volumeID uint) ([]models.VolumeBackup, error) {
	return s.repo.ListByVolume(volumeID)
}

// Configured reports whether the workspace has a usable S3 backup target (S3
// enabled with a bucket). Volume backups are blocked when this is false; the UI
// uses it to disable the action before the user triggers a 400.
func (s *Service) Configured(workspaceID uint) bool {
	cfg, _, err := s.target(workspaceID)
	return err == nil && cfg != nil
}

// Delete removes a volume backup: the archive object first, then the record. That order leaves no
// object a retry cannot reach, since the row is what names it. An object that is already gone counts
// as deleted; any other failure is logged and the row is removed anyway, because a backup whose
// bucket credentials have since been rotated must not become undeletable.
func (s *Service) Delete(ctx context.Context, b *models.VolumeBackup) error {
	s.deleteArchive(ctx, b)
	return s.repo.Delete(b.ID)
}

func (s *Service) deleteArchive(ctx context.Context, b *models.VolumeBackup) {
	if b.Filename == "" {
		return
	}
	store, err := s.store(b.WorkspaceID)
	if err != nil {
		logger.Error("volume backup: open object store to delete archive", "volume_backup", b.ID, "error", err)
		return
	}
	keys := []string{archiveKey(b.S3Path, b.Filename)}
	if b.IsPoint() {
		keys = append(keys, archiveKey(b.S3Path, backup.SetInfoObject))
	}
	for _, key := range keys {
		if err := store.Delete(ctx, key); err != nil && !errors.Is(err, blob.ErrNotFound) {
			logger.Error("volume backup: delete archive object", "object", key, "error", err)
		}
	}
}

// store opens the workspace's object store for direct bucket work — sizing and deleting archives,
// which volume-bkup itself cannot do (it has no list or delete command).
func (s *Service) store(workspaceID uint) (*blob.Store, error) {
	cfg, _, err := s.target(workspaceID)
	if err != nil {
		return nil, err
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

// archiveKey is the object key an archive was uploaded under: the remote prefix the run used, plus
// the name the helper reported.
func archiveKey(prefix, filename string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return filename
	}
	return path.Join(prefix, filename)
}

// recordSize looks the uploaded archive up in the bucket and records its size. The bucket is the
// authority — the helper's output does not report a size — and finding the object also confirms the
// row names something that exists. A lookup failure never fails a backup that succeeded.
func (s *Service) recordSize(ctx context.Context, b *models.VolumeBackup) {
	store, err := s.store(b.WorkspaceID)
	if err != nil {
		logger.Warn("volume backup: open object store to size archive", "volume_backup", b.ID, "error", err)
		return
	}
	key := archiveKey(b.S3Path, b.Filename)
	objs, err := store.List(ctx, key)
	if err != nil {
		logger.Warn("volume backup: size archive", "object", key, "error", err)
		return
	}
	for _, o := range objs {
		if o.Key == key {
			b.SizeBytes = o.Size
			return
		}
	}
	logger.Warn("volume backup: the archive is not in the bucket", "object", key, "volume_backup", b.ID)
}

// oneShotName builds a unique container name for a helper run. The random suffix is what keeps a
// retry from colliding with the container its predecessor left behind; a timestamp is not enough,
// since two runs in the same clock tick would produce the same name and Docker refuses the second.
func oneShotName(prefix string, id uint) string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s-%d-%d", prefix, id, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%d-%s", prefix, id, hex.EncodeToString(buf))
}

// helperNetworks attaches the archive helper to the networks the object store may live on. The proxy
// network comes first: Docker picks the default route from the first attachment, and the archive
// still has to reach an S3 endpoint that is off-box.
func (s *Service) helperNetworks(ctx context.Context, dc docker.Client) ([]string, error) {
	var out []string
	for _, name := range []string{s.network, s.internalNetwork} {
		if name == "" || slices.Contains(out, name) {
			continue
		}
		if _, err := dc.EnsureNetwork(ctx, name); err != nil {
			return nil, fmt.Errorf("attach the volume backup container to network %q (it reaches the object store there): %w", name, err)
		}
		out = append(out, name)
	}
	return out, nil
}

// Create records a pending plain archive and enqueues it for the background
// worker, returning the pending record immediately. With no enqueuer wired it
// runs the backup synchronously. Validates that S3 is configured.
func (s *Service) Create(ctx context.Context, vol *models.Volume, trigger string) (*models.VolumeBackup, error) {
	return s.create(ctx, vol, trigger, false, nil)
}

// CreatePoint is Create for a recovery point: a ref, its own prefix and descriptor, sealing
// under the workspace passphrase, and verification once it lands. The caller owns the licence
// check. scheduleID, when set, has the schedule's retention applied after a successful run.
func (s *Service) CreatePoint(ctx context.Context, vol *models.Volume, trigger string, scheduleID *uint) (*models.VolumeBackup, error) {
	return s.create(ctx, vol, trigger, true, scheduleID)
}

func (s *Service) create(ctx context.Context, vol *models.Volume, trigger string, point bool, scheduleID *uint) (*models.VolumeBackup, error) {
	cfg, path, err := s.target(vol.WorkspaceID)
	if err != nil {
		return nil, err
	}
	b := &models.VolumeBackup{
		WorkspaceID: vol.WorkspaceID, VolumeID: vol.ID, ServerID: vol.ServerID,
		VolumeName: vol.DockerName, Status: models.BackupPending, Trigger: trigger,
		S3Bucket: cfg.Bucket, S3Path: path,
	}
	if point {
		b.Ref = models.NewVolumeBackupRef(vol.Name, time.Now())
		b.S3Path = PointPrefix(path, vol.Name, b.Ref)
		b.Consistency = models.VolumeConsistencyHot
		b.ScheduleID = scheduleID
	}
	if err := s.repo.Create(b); err != nil {
		return nil, err
	}
	if s.enqueuer == nil {
		// No worker wired — run inline.
		_ = s.RunBackup(ctx, b.ID)
		return s.repo.FindByID(b.ID)
	}
	if err := s.enqueuer.EnqueueVolumeBackup(b.ID, vol.ServerID); err != nil {
		return s.fail(b, fmt.Errorf("enqueue backup: %w", err)), nil
	}
	return b, nil
}

// RunBackup executes a pending volume backup (the worker entry point): it loads
// the record, archives the volume to S3, and records the outcome. Handled
// failures are recorded on the row and return nil (no auto-retry).
func (s *Service) RunBackup(ctx context.Context, backupID uint) error {
	b, err := s.repo.FindByID(backupID)
	if err != nil {
		return fmt.Errorf("volume backup %d not found: %w", backupID, err)
	}
	if b.Status == models.BackupCompleted || b.Status == models.BackupFailed {
		return nil // already processed
	}
	vol, err := s.volumes.FindInWorkspace(b.WorkspaceID, b.VolumeID)
	if err != nil {
		s.fail(b, fmt.Errorf("volume not found: %w", err))
		return nil
	}
	cfg, _, err := s.target(vol.WorkspaceID)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	dc, err := s.clients.For(vol.ServerID)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	env := append(backup.S3Env(cfg), backup.IndexEnv(vol.Name))
	var passphrase, recipients, readKey string
	if b.IsPoint() {
		if passphrase, err = s.passphrase(b.WorkspaceID); err != nil {
			s.fail(b, fmt.Errorf("read the workspace backup passphrase: %w", err))
			return nil
		}
		var identity string
		if recipients, identity, err = s.age(b.WorkspaceID); err != nil {
			s.fail(b, fmt.Errorf("read the workspace age settings: %w", err))
			return nil
		}
		switch {
		case recipients != "":
			// age needs no envelope: the recipients are public keys, only the identity opens the archive.
			env = append(env, "ENCRYPTION=age", "AGE_RECIPIENTS="+recipients)
			if identity != "" {
				readKey = "AGE_IDENTITY=" + identity
			}
		case passphrase != "":
			dataKey, sealed, err := newSealedKey(passphrase)
			if err != nil {
				s.fail(b, err)
				return nil
			}
			b.Envelope = sealed
			env = append(env, "GPG_PASSPHRASE="+dataKey)
			readKey = "GPG_PASSPHRASE=" + dataKey
		}
	}

	now := time.Now()
	b.Status = models.BackupRunning
	b.StartedAt = &now
	_ = s.repo.Update(b)

	image := s.image()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		s.fail(b, fmt.Errorf("pull image: %w", err))
		return nil
	}
	nets, err := s.helperNetworks(ctx, dc)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:  oneShotName("mb-volbkup", b.ID),
		Image: image,
		Env:   env,
		Cmd:   []string{"backup", "--storage", "s3", "--remote-path", b.S3Path, "--name", vol.Name},
		// Read-only: the archiver reads the volume and uploads it, and must not be able to write to
		// the data it is protecting.
		Mounts:         map[string]string{vol.DockerName: volumeMount},
		ReadOnlyMounts: []string{vol.DockerName},
		Networks:       nets,
		Labels: map[string]string{
			docker.LabelWorkspace: fmt.Sprintf("%d", vol.WorkspaceID),
			docker.LabelVolume:    fmt.Sprintf("%d", vol.ID),
		},
	})
	b.Logs = out
	if err != nil {
		s.fail(b, err)
		return nil
	}
	if exit != 0 {
		s.fail(b, fmt.Errorf("volume backup exited %d", exit))
		return nil
	}

	name, encrypted, err := backup.ArtifactName(out, archiveRe)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	b.Filename = name
	b.Encrypted = encrypted
	if recipients != "" && !encrypted {
		logger.Warn("volume recovery point stored UNENCRYPTED despite age encryption: the volume-bkup image does not support it — upgrade it",
			"volume", vol.ID, "artifact", name)
	}
	if b.Envelope != "" && !encrypted {
		// An envelope over a cleartext archive seals nothing, and would still block clearing the
		// passphrase as if it did.
		logger.Warn("volume recovery point stored UNENCRYPTED despite a passphrase: the volume-bkup image does not support encryption — upgrade it",
			"volume", vol.ID, "artifact", name)
		b.Envelope = ""
	}
	b.Checksum = backup.ArtifactSHA256(out)
	if size := backup.ArtifactSize(out); size > 0 {
		b.SizeBytes = size
	} else {
		// 1.x images do not report a size: look the archive up instead.
		s.recordSize(ctx, b)
	}
	readBack, readBackErr := "", ""
	if s.s3 != nil && s.s3.VerifyAfterBackup(b.WorkspaceID) && (!encrypted || readKey != "") {
		venv := append(backup.S3Env(cfg), backup.IndexEnv(vol.Name))
		if encrypted {
			venv = append(venv, readKey)
		}
		vexit, vout, verr := dc.RunOneShot(ctx, docker.RunSpec{
			Name:     oneShotName("mb-volverify", b.ID),
			Image:    image,
			Env:      venv,
			Cmd:      []string{"verify", "--storage", "s3", "--remote-path", b.S3Path, "--file", name},
			Networks: nets,
		})
		readBack, readBackErr = backup.VerifyOutcome(vexit, vout, verr)
	}
	if !b.IsPoint() && readBack != "" {
		vt := time.Now()
		b.VerifiedAt, b.VerifyStatus, b.VerifyError = &vt, readBack, readBackErr
	}
	fin := time.Now()
	b.Status = models.BackupCompleted
	b.FinishedAt = &fin
	if err := s.repo.Update(b); err != nil {
		return err
	}
	s.externalizeLog(b)
	logger.Info("volume backup completed", "volume", vol.ID, "file", b.Filename)
	if b.IsPoint() {
		s.finishPoint(ctx, vol, b, passphrase, readBack, readBackErr)
	}
	return nil
}

func (s *Service) age(workspaceID uint) (recipients, identity string, err error) {
	if s.s3 == nil {
		return "", "", nil
	}
	return s.s3.BackupAge(workspaceID)
}

// Restore extracts a previously-created archive back into the volume, mounting
// it read-write at /data. This overwrites existing data.
func (s *Service) Restore(ctx context.Context, vol *models.Volume, b *models.VolumeBackup) error {
	if b.Filename == "" {
		return ErrNoArchive
	}
	cfg, _, err := s.target(vol.WorkspaceID)
	if err != nil {
		return err
	}
	dc, err := s.clients.For(vol.ServerID)
	if err != nil {
		return err
	}
	image := s.image()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	nets, err := s.helperNetworks(ctx, dc)
	if err != nil {
		return err
	}
	env := backup.S3Env(cfg)
	var key, identity string
	if strings.HasSuffix(b.Filename, ".age") {
		if _, identity, err = s.age(b.WorkspaceID); err != nil {
			return err
		}
	} else if key, err = s.archiveKeyPassphrase(b); err != nil {
		return err
	}
	keys, err := backup.RestoreKeyEnv(b.Filename, key, identity)
	if err != nil {
		return err
	}
	env = append(env, keys...)
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:     oneShotName("mb-volrestore", b.ID),
		Image:    image,
		Env:      env,
		Cmd:      []string{"restore", "--storage", "s3", "--remote-path", b.S3Path, "--file", b.Filename},
		Mounts:   map[string]string{vol.DockerName: volumeMount},
		Networks: nets,
	})
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("volume restore exited %d: %s", exit, out)
	}
	return nil
}

// target resolves the workspace's S3 config + volume path, returning
// ErrS3NotConfigured when S3 is not set up.
func (s *Service) target(workspaceID uint) (*backup.S3Config, string, error) {
	if s.s3 == nil {
		return nil, "", ErrS3NotConfigured
	}
	cfg, path, err := s.s3.VolumeBackupTarget(workspaceID)
	if err != nil {
		return nil, "", err
	}
	if cfg == nil {
		return nil, "", ErrS3NotConfigured
	}
	return cfg, path, nil
}

// fail marks a backup record failed and returns it.
func (s *Service) fail(b *models.VolumeBackup, cause error) *models.VolumeBackup {
	fin := time.Now()
	b.Status = models.BackupFailed
	b.Error = cause.Error()
	b.FinishedAt = &fin
	if b.Filename == "" {
		// Nothing was stored under it, and a live envelope would block clearing the passphrase.
		b.Envelope = ""
	}
	_ = s.repo.Update(b)
	s.externalizeLog(b)
	logger.Error("volume backup failed", "volume", b.VolumeID, "error", cause)
	if b.IsPoint() && s.alerter != nil {
		s.alerter.VolumeBackupFailed(b.WorkspaceID, b.VolumeID, b.VolumeName, b.Ref, b.Error)
	}
	return b
}
