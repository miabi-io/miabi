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

// Package platformbackup is admin-only control-plane disaster recovery; artifacts use the backup passphrase, not the master key.
package platformbackup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	// Volume backups have no local destination, like volume-bkup.
	ErrS3NotConfigured = errors.New("platform backup requires an S3/MinIO target; configure one in Admin → Platform Backup or via MIABI_PLATFORM_BACKUP_S3_*")
	// ErrNoArtifact is returned when restoring/downloading a backup with no file.
	ErrNoArtifact = errors.New("platform backup has no artifact")
	// Artifacts live in the operator's bucket; Miabi does not proxy them back out.
	ErrDownloadRemote = errors.New("platform backup artifacts are stored in your S3 bucket; fetch them from there")
	// ErrUnknownSubject is returned for an unrecognized backup subject.
	ErrUnknownSubject = errors.New("unknown platform backup subject")

	ErrVolumeExcluded = errors.New("this volume cannot be included in a platform backup")
	// ErrNoPassphrase is returned when encryption is on but no passphrase is set.
	ErrNoPassphrase = errors.New("backup encryption is enabled but no backup passphrase is set")
	// A recovery point stored only on the host it protects is not a recovery point.
	ErrSetNeedsS3 = errors.New("a platform recovery point requires an S3 target: a backup stored only on the host you are recovering from is no disaster recovery at all")

	dbArtifactRe  = backup.DBArtifactRe
	volArtifactRe = backup.VolumeArtifactRe
)

const (
	volumeMount = "/data"

	// No longer written to, but kept so the volume stays excluded and old rows are recognized.
	legacyLocalVolume = "mb-platform-backups"

	// A backup on the host it protects is unreadable once that host is gone, the one case this exists for.
	destS3 = "s3"

	defaultPgImage  = platformimage.DefaultPgBkup
	defaultVolImage = platformimage.DefaultVolumeBkup
)

func artifactName(out string, re *regexp.Regexp) (name string, encrypted bool, err error) {
	return backup.ArtifactName(out, re)
}

func notEncrypted(subject, name string) {
	logger.Warn("artifact stored UNENCRYPTED despite encryption being enabled: the backup helper does not support it — upgrade the image",
		"subject", subject, "artifact", name)
}

// The helper reports problems on stdout with a nil RunOneShot error, so carry its output as the diagnosis.
func oneShotError(action string, exit int, out string, err error) error {
	detail := strings.TrimSpace(out)
	if detail == "" {
		detail = "(no output)"
	}
	// Keep the tail: these tools print failures last, and the row is not a log file.
	if len(detail) > 2000 {
		detail = "…" + detail[len(detail)-2000:]
	}
	if err != nil {
		return fmt.Errorf("%s could not run: %w (output: %s)", action, err, detail)
	}
	return fmt.Errorf("%s exited with code %d: %s", action, exit, detail)
}

// The helper's loopback is its own container, so a loopback host could never work and would look like the DB is down.
func (s *Service) assertDBReachable() error {
	switch strings.ToLower(strings.TrimSpace(s.db.Host)) {
	case "", "localhost", "127.0.0.1", "::1", "[::1]":
		return fmt.Errorf(
			"the control-plane database host is %q. The backup runs in its own container, where that address means "+
				"the container itself — not the machine Miabi runs on — so it can never connect. Set MIABI_DB_HOST to "+
				"an address the container can reach: the Postgres container's name when Miabi is installed as a stack "+
				"(%q, on the %s network), or host.docker.internal when Postgres runs on the host and Docker provides "+
				"that name",
			s.db.Host, "miabi-postgres", s.dbNetworkName())
	}
	return nil
}

// On managed installs the DB is on the private network, not the proxy one.
func (s *Service) dbNetworkName() string {
	if s.internalNetwork != "" {
		return s.internalNetwork
	}
	if s.network == "" {
		return "platform"
	}
	return s.network
}

type DBConn struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string
}

// NodeDocker resolves the Docker client for a node id (0 = local manager node).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// When unset, backups run synchronously (tests / no Redis).
type Enqueuer interface {
	EnqueuePlatformBackup(backupID uint) error
}

// Service backs up and restores the platform's own database and volumes.
type Service struct {
	repo     *repositories.PlatformBackupRepository
	sets     *repositories.PlatformBackupSetRepository
	settings *repositories.PlatformBackupSettingsRepository
	clients  NodeDocker
	db       DBConn
	network  string // proxy network attached so pg-bkup can reach a managed DB by name
	// Empty on Compose; the helper joins both, since the DB is only here and a self-hosted MinIO only on the other.
	internalNetwork string
	images          ImageResolver
	enqueuer        Enqueuer
	logs            *logstore.Store
	identity        IdentitySource
	fingerprint     func(label string) string
	// For an arbitrary key, so a key recovered from an identity envelope can be checked against a set.
	keyFingerprint func(key, label string) string
	// Environment config wins over the stored settings row (see settings.go).
	env config.PlatformBackupConfig
	// tenants enumerates and dumps workspace data (see tenant.go). Optional.
	tenants TenantSource
	// Optional; without it a volume restore runs with the apps still attached.
	apps AppStopper
}

// network is the proxy network the DB-backup container joins to reach Postgres by service name.
func NewService(repo *repositories.PlatformBackupRepository, sets *repositories.PlatformBackupSetRepository, settings *repositories.PlatformBackupSettingsRepository, clients NodeDocker, db DBConn, network string) *Service {
	return &Service{repo: repo, sets: sets, settings: settings, clients: clients, db: db, network: network}
}

// SetInternalNetwork names the private network holding the DB on managed installs; leave unset on Compose.
func (s *Service) SetInternalNetwork(name string) { s.internalNetwork = name }

// SetImageResolver wires the deployment-config resolver for the backup tool images.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

// Platform backups have no workspace, so their log objects live under an admin-only prefix.
func (s *Service) SetLogStore(store *logstore.Store) { s.logs = store }

// On any error the full log stays in the DB tail.
func (s *Service) externalizeLog(b *models.PlatformBackup) {
	if !s.logs.Enabled() || b.LogRef != "" {
		return
	}
	ref := logstore.PlatformBackupRef(b.ID)
	res, err := s.logs.Externalize(ref, b.Logs)
	if err != nil {
		logger.Error("log store: externalize platform backup log failed", "platform_backup", b.ID, "error", err)
		return
	}
	if err := s.repo.SetLogMeta(b.ID, res.Ref, res.Tail, res.Bytes, res.Lines, res.Truncated); err != nil {
		logger.Error("log store: record platform backup log ref failed", "platform_backup", b.ID, "error", err)
		return
	}
	b.LogRef, b.Logs = res.Ref, res.Tail
	b.LogBytes, b.LogLines, b.LogTruncated = res.Bytes, res.Lines, res.Truncated
}

// SetEnqueuer wires the background worker producer.
func (s *Service) SetEnqueuer(e Enqueuer) { s.enqueuer = e }

func (s *Service) pgImage() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyBackupPostgres); r != "" {
			return r
		}
	}
	return defaultPgImage
}

func (s *Service) volImage() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyBackupVolume); r != "" {
			return r
		}
	}
	return defaultVolImage
}

// Proxy network first: it has egress on hardened installs, and Docker picks the default route from attachments.
func (s *Service) backupNetworks(ctx context.Context, dc docker.Client) ([]string, error) {
	var out []string
	for _, name := range []string{s.network, s.internalNetwork} {
		if name == "" || slices.Contains(out, name) {
			continue
		}
		if _, err := dc.EnsureNetwork(ctx, name); err != nil {
			return nil, fmt.Errorf("attach the backup container to network %q (it reaches the database by name there): %w", name, err)
		}
		out = append(out, name)
	}
	return out, nil
}

func (s *Service) docker() (docker.Client, error) { return s.clients.For(0) }

// List returns all platform backups, newest first.
func (s *Service) List() ([]models.PlatformBackup, error) { return s.repo.List() }

// ListPaged returns a page of platform backups plus the total count.
func (s *Service) ListPaged(limit, offset int) ([]models.PlatformBackup, int64, error) {
	return s.repo.ListPaged(limit, offset)
}

// Get returns a single platform backup.
func (s *Service) Get(id uint) (*models.PlatformBackup, error) { return s.repo.FindByID(id) }

// Create runs synchronously when no enqueuer is wired; volume backups require an S3 target.
func (s *Service) Create(ctx context.Context, subject models.PlatformBackupSubject, volumeName, trigger string) (*models.PlatformBackup, error) {
	st, err := s.getSettings()
	if err != nil {
		return nil, err
	}
	cfg, err := s.s3Config(st)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, ErrS3NotConfigured
	}
	switch subject {
	case models.PlatformBackupDatabase:
	case models.PlatformBackupVolume:
		if volumeName == "" {
			return nil, errors.New("volume name is required for a volume backup")
		}
		if err := s.assertBackupable(ctx, volumeName); err != nil {
			return nil, err
		}
	case models.PlatformBackupIdentity:
		return nil, errors.New("the identity envelope is produced as part of a recovery point, not on its own")
	default:
		return nil, ErrUnknownSubject
	}

	b := &models.PlatformBackup{
		Subject: subject, VolumeName: volumeName,
		Status: models.BackupPending, Trigger: trigger, Destination: destS3,
		S3Bucket: cfg.Bucket,
		S3Path:   st.DatabaseBackupPath,
	}
	if subject == models.PlatformBackupVolume {
		b.S3Path = st.VolumeBackupPath
	}
	if err := s.repo.Create(b); err != nil {
		return nil, err
	}
	if s.enqueuer == nil {
		_ = s.RunBackup(ctx, b.ID)
		return s.repo.FindByID(b.ID)
	}
	if err := s.enqueuer.EnqueuePlatformBackup(b.ID); err != nil {
		return s.fail(b, fmt.Errorf("enqueue backup: %w", err)), nil
	}
	return b, nil
}

func (s *Service) RunBackup(ctx context.Context, backupID uint) error {
	b, err := s.repo.FindByID(backupID)
	if err != nil {
		return fmt.Errorf("platform backup %d not found: %w", backupID, err)
	}
	if b.Status == models.BackupCompleted || b.Status == models.BackupFailed {
		return nil // already processed
	}
	st, err := s.getSettings()
	if err != nil {
		s.fail(b, err)
		return nil
	}
	switch b.Subject {
	case models.PlatformBackupDatabase:
		return s.runDBBackup(ctx, b, st)
	case models.PlatformBackupVolume:
		return s.runVolumeBackup(ctx, b, st)
	case models.PlatformBackupIdentity:
		return s.runIdentityBackup(ctx, b, st)
	case models.PlatformBackupTenantDatabase:
		return s.runTenantDatabaseBackup(ctx, b, st)
	case models.PlatformBackupTenantVolume:
		return s.runTenantVolumeBackup(ctx, b, st)
	default:
		s.fail(b, ErrUnknownSubject)
		return nil
	}
}

// Labels are resolved from Docker so a workspace volume without an "mb-" prefix is still caught.
func (s *Service) assertBackupable(ctx context.Context, name string) error {
	var labels map[string]string
	if dc, err := s.docker(); err == nil {
		if vols, err := dc.ListVolumes(ctx); err == nil {
			for _, v := range vols {
				if v.Name == name {
					labels = v.Labels
					break
				}
			}
		}
	}
	if !excludedVolume(name, labels) {
		return nil
	}
	if models.IsRegistryDataVolume(name) {
		return fmt.Errorf("%w: %s holds live registry blob storage — archiving it under concurrent pushes produces an archive that restores but cannot be pulled from. Run the registry on S3 storage so images survive losing this host", ErrVolumeExcluded, name)
	}
	return fmt.Errorf("%w: %s", ErrVolumeExcluded, name)
}

func (s *Service) runDBBackup(ctx context.Context, b *models.PlatformBackup, st *models.PlatformBackupSettings) error {
	dc, err := s.docker()
	if err != nil {
		s.fail(b, err)
		return nil
	}
	cfg, err := s.s3Config(st)
	if err != nil {
		s.fail(b, err)
		return nil
	}

	now := time.Now()
	b.Status = models.BackupRunning
	b.StartedAt = &now
	_ = s.repo.Update(b)

	gpg, wantEncryption, err := s.gpgEnv(st)
	if err != nil {
		s.fail(b, err)
		return nil
	}

	if cfg == nil {
		s.fail(b, ErrS3NotConfigured)
		return nil
	}

	if err := s.assertDBReachable(); err != nil {
		s.fail(b, err)
		return nil
	}

	env := append(append(s.dbEnv(), gpg...), backup.S3Env(cfg)...)
	env = append(env, backup.IndexEnv("platform"))
	cmd := []string{"backup", "--storage", "s3", "-d", s.db.Name}
	if st.DatabaseBackupPath != "" {
		cmd = append(cmd, "--path", st.DatabaseBackupPath)
	}
	// Unattached, the container cannot resolve the DB host and fails far from here with an unexplained exit 1.
	nets, err := s.backupNetworks(ctx, dc)
	if err != nil {
		s.fail(b, err)
		return nil
	}

	image := s.pgImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		s.fail(b, fmt.Errorf("pull backup image: %w", err))
		return nil
	}
	out, err := s.runHelper(ctx, dc, "control-plane database backup", docker.RunSpec{
		Name:     fmt.Sprintf("mb-platform-dbbkup-%d", b.ID),
		Image:    image,
		Env:      env,
		Cmd:      cmd,
		Networks: nets,
		Labels:   map[string]string{docker.LabelManaged: "true"},
	})
	b.Logs = out
	if err != nil {
		s.fail(b, err)
		return nil
	}
	name, encrypted, err := artifactName(out, dbArtifactRe)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	if wantEncryption && !encrypted {
		notEncrypted(string(b.Subject), name)
	}
	b.Filename = name
	b.Encrypted = encrypted
	fin := time.Now()
	b.Status = models.BackupCompleted
	b.FinishedAt = &fin
	if err := s.repo.Update(b); err != nil {
		return err
	}
	s.externalizeLog(b)
	s.finalizeSet(b.SetID)
	logger.Info("platform database backup completed", "backup", b.ID, "destination", b.Destination, "encrypted", encrypted, "file", b.Filename)
	return nil
}

func (s *Service) runVolumeBackup(ctx context.Context, b *models.PlatformBackup, st *models.PlatformBackupSettings) error {
	dc, err := s.docker()
	if err != nil {
		s.fail(b, err)
		return nil
	}
	cfg, err := s.s3Config(st)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	if cfg == nil {
		s.fail(b, ErrS3NotConfigured)
		return nil
	}

	gpg, wantEncryption, err := s.gpgEnv(st)
	if err != nil {
		s.fail(b, err)
		return nil
	}

	now := time.Now()
	b.Status = models.BackupRunning
	b.StartedAt = &now
	_ = s.repo.Update(b)

	image := s.volImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		s.fail(b, fmt.Errorf("pull image: %w", err))
		return nil
	}
	// This container uploads, and a self-hosted MinIO usually resolves only on the platform network.
	nets, err := s.backupNetworks(ctx, dc)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	out, err := s.runHelper(ctx, dc, "volume backup of "+b.VolumeName, docker.RunSpec{
		Name:     fmt.Sprintf("mb-platform-volbkup-%d", b.ID),
		Image:    image,
		Env:      append(append(backup.S3Env(cfg), gpg...), backup.IndexEnv("platform-"+volumeArchiveName(b.VolumeName))),
		Cmd:      []string{"backup", "--storage", "s3", "--remote-path", st.VolumeBackupPath, "--name", volumeArchiveName(b.VolumeName)},
		Mounts:   map[string]string{b.VolumeName: volumeMount},
		Networks: nets,
		Labels:   map[string]string{docker.LabelManaged: "true"},
	})
	b.Logs = out
	if err != nil {
		s.fail(b, err)
		return nil
	}
	name, encrypted, err := artifactName(out, volArtifactRe)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	if wantEncryption && !encrypted {
		notEncrypted(string(b.Subject)+" "+b.VolumeName, name)
	}
	b.Filename = name
	b.Encrypted = encrypted
	fin := time.Now()
	b.Status = models.BackupCompleted
	b.FinishedAt = &fin
	if err := s.repo.Update(b); err != nil {
		return err
	}
	s.externalizeLog(b)
	s.finalizeSet(b.SetID)
	logger.Info("platform volume backup completed", "backup", b.ID, "volume", b.VolumeName, "encrypted", encrypted, "file", b.Filename)
	return nil
}

// Restore is synchronous: the admin confirms a destructive, maintenance-mode operation and waits.
func (s *Service) Restore(ctx context.Context, b *models.PlatformBackup) error {
	if b.Filename == "" {
		return ErrNoArtifact
	}
	st, err := s.getSettings()
	if err != nil {
		return err
	}
	switch b.Subject {
	case models.PlatformBackupDatabase:
		return s.restoreDB(ctx, b, st)
	case models.PlatformBackupVolume:
		return s.restoreVolume(ctx, b, st)
	default:
		return ErrUnknownSubject
	}
}

func (s *Service) restoreDB(ctx context.Context, b *models.PlatformBackup, st *models.PlatformBackupSettings) error {
	dc, err := s.docker()
	if err != nil {
		return err
	}
	gpg, err := s.restoreGPGEnv(b, st)
	if err != nil {
		return err
	}

	cfg, err := s.s3Config(st)
	if err != nil {
		return err
	}
	if cfg == nil {
		return ErrS3NotConfigured
	}
	env := append(append(s.dbEnv(), gpg...), backup.S3Env(cfg)...)
	cmd := []string{"restore", "--storage", "s3", "-d", s.db.Name, "-f", b.Filename}
	if b.S3Path != "" {
		cmd = append(cmd, "--path", b.S3Path)
	}
	// Best-effort: the tool's own connection error says more than a network we could not create.
	nets, _ := s.backupNetworks(ctx, dc)

	image := s.pgImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull backup image: %w", err)
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:     fmt.Sprintf("mb-platform-dbrestore-%d", b.ID),
		Image:    image,
		Env:      env,
		Cmd:      cmd,
		Networks: nets,
		Labels:   map[string]string{docker.LabelManaged: "true"},
	})
	if err != nil || exit != 0 {
		return fmt.Errorf("restore exited with code %d: %s", exit, out)
	}
	logger.Info("platform database restore completed", "backup", b.ID)
	return nil
}

func (s *Service) restoreVolume(ctx context.Context, b *models.PlatformBackup, st *models.PlatformBackupSettings) error {
	if b.VolumeName == "" {
		return errors.New("volume backup has no target volume")
	}
	cfg, err := s.s3Config(st)
	if err != nil {
		return err
	}
	if cfg == nil {
		return ErrS3NotConfigured
	}
	gpg, err := s.restoreGPGEnv(b, st)
	if err != nil {
		return err
	}
	dc, err := s.docker()
	if err != nil {
		return err
	}
	image := s.volImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	nets, err := s.backupNetworks(ctx, dc)
	if err != nil {
		return err
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:     fmt.Sprintf("mb-platform-volrestore-%d", b.ID),
		Image:    image,
		Env:      append(backup.S3Env(cfg), gpg...),
		Cmd:      []string{"restore", "--storage", "s3", "--remote-path", b.S3Path, "--file", b.Filename},
		Mounts:   map[string]string{b.VolumeName: volumeMount},
		Networks: nets,
		Labels:   map[string]string{docker.LabelManaged: "true"},
	})
	if err != nil || exit != 0 {
		return fmt.Errorf("volume restore exited with code %d: %s", exit, out)
	}
	logger.Info("platform volume restore completed", "backup", b.ID, "volume", b.VolumeName)
	return nil
}

// A record removed without its artifact leaves an orphaned file paying storage forever.
func (s *Service) Delete(ctx context.Context, b *models.PlatformBackup) error {
	if b.Filename != "" {
		st, err := s.getSettings()
		if err == nil {
			if store, err := s.blobStore(st); err == nil {
				if err := store.Delete(ctx, objectKey(b)); err != nil {
					logger.Error("remove platform backup artifact", "backup", b.ID, "object", objectKey(b), "error", err)
				}
			}
		}
	}
	return s.repo.Delete(b.ID)
}

// Download is unsupported: artifacts are in the operator's bucket, reachable with their own S3 client.
func (s *Service) Download(_ context.Context, _ *models.PlatformBackup) (io.ReadCloser, int64, string, error) {
	return nil, 0, "", ErrDownloadRemote
}

// A zero bound is ignored.
func (s *Service) Prune(ctx context.Context, maxBackups, retentionDays int) (int, error) {
	if maxBackups <= 0 && retentionDays <= 0 {
		return 0, nil
	}
	backups, err := s.repo.List() // newest-first
	if err != nil {
		return 0, err
	}
	var cutoff time.Time
	if retentionDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -retentionDays)
	}
	removed := 0
	kept := 0
	for i := range backups {
		b := &backups[i]
		// Set-owned artifacts are pruned as a unit by PruneSets; deleting one would hollow out a restorable set.
		if b.SetID != nil {
			continue
		}
		overCount := maxBackups > 0 && kept >= maxBackups
		kept++
		tooOld := retentionDays > 0 && b.CreatedAt.Before(cutoff)
		if overCount || tooOld {
			if err := s.Delete(ctx, b); err != nil {
				logger.Error("prune platform backup", "backup", b.ID, "error", err)
				continue
			}
			removed++
		}
	}
	if removed > 0 {
		logger.Info("pruned platform backups", "removed", removed)
	}
	return removed, nil
}

// RunScheduled takes a whole recovery point with S3; with only local storage it falls back to legacy per-artifact backups.
func (s *Service) RunScheduled(ctx context.Context) error {
	st, err := s.getSettings()
	if err != nil {
		return err
	}
	if s.S3Configured() {
		if _, err := s.CreateSet(ctx, "scheduled"); err != nil {
			logger.Error("scheduled platform recovery point failed", "error", err)
		}
		if st.MaxBackups > 0 || st.RetentionDays > 0 {
			_, _ = s.PruneSets(ctx, st.MaxBackups, st.RetentionDays)
		}
		return nil
	}

	logger.Warn("platform backup has no S3 target: taking a local database backup only, which cannot restore this host after it is lost")
	if _, err := s.Create(ctx, models.PlatformBackupDatabase, "", "scheduled"); err != nil {
		logger.Error("scheduled platform database backup failed", "error", err)
	}
	if st.MaxBackups > 0 || st.RetentionDays > 0 {
		_, _ = s.Prune(ctx, st.MaxBackups, st.RetentionDays)
	}
	return nil
}

// Role is the io.miabi.role label value when the volume is infra.
type PlatformVolume struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

// DiscoverVolumes excludes backup volumes, per-workspace volumes, and the registry data volume.
func (s *Service) DiscoverVolumes(ctx context.Context) ([]PlatformVolume, error) {
	dc, err := s.docker()
	if err != nil {
		return nil, err
	}
	vols, err := dc.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PlatformVolume, 0, len(vols))
	for _, v := range vols {
		if excludedVolume(v.Name, v.Labels) {
			continue
		}
		// Limit to Miabi-managed/named volumes; ignore unrelated host volumes.
		if !docker.IsManaged(v.Labels) && !strings.HasPrefix(v.Name, "mb-") {
			continue
		}
		role, _ := docker.LabelValue(v.Labels, docker.LabelRole)
		out = append(out, PlatformVolume{Name: v.Name, Role: role})
	}
	return out, nil
}

// Separate so the rule also applies when a caller names a volume directly via the API.
func excludedVolume(name string, labels map[string]string) bool {
	if name == legacyLocalVolume || strings.HasPrefix(name, "mb-backups-") {
		return true
	}
	if models.IsRegistryDataVolume(name) {
		return true
	}
	if _, ok := docker.LabelValue(labels, docker.LabelWorkspace); ok {
		return true
	}
	return false
}

func (s *Service) dbEnv() []string {
	return []string{
		"DB_HOST=" + s.db.Host,
		fmt.Sprintf("DB_PORT=%d", s.db.Port),
		"DB_NAME=" + s.db.Name,
		"DB_USERNAME=" + s.db.User,
		"DB_PASSWORD=" + s.db.Password,
	}
}

func (s *Service) fail(b *models.PlatformBackup, cause error) *models.PlatformBackup {
	fin := time.Now()
	b.Status = models.BackupFailed
	b.Error = cause.Error()
	b.FinishedAt = &fin
	_ = s.repo.Update(b)
	s.externalizeLog(b)
	// Without this a failed item left its set "running" forever: never completed, failed or pruned.
	s.finalizeSet(b.SetID)
	logger.Error("platform backup failed", "backup", b.ID, "subject", b.Subject, "error", cause)
	return b
}

// Keeps only alphanumerics, dash and underscore for volume-bkup --name.
func volumeArchiveName(volume string) string {
	var b strings.Builder
	for _, r := range volume {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := b.String()
	if name == "" {
		name = "platform-volume"
	}
	return name
}
