// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package platformbackup is the admin-only disaster-recovery feature for Miabi's own control plane. It
// reuses the per-workspace backup primitives but draws its database connection from control-plane config
// and runs on the manager node. Artifacts are GPG-encrypted with the backup passphrase, not the master key.
package platformbackup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
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
	// ErrS3NotConfigured is returned when an operation needs an S3 target that is
	// not set up (volume backups have no local destination, like volume-bkup).
	ErrS3NotConfigured = errors.New("platform backup requires an S3/MinIO target; configure one in Admin → Platform Backup or via MIABI_PLATFORM_BACKUP_S3_*")
	// ErrNoArtifact is returned when restoring/downloading a backup with no file.
	ErrNoArtifact = errors.New("platform backup has no artifact")
	// ErrDownloadRemote is returned by the download endpoint: artifacts live in
	// the operator's bucket, and Miabi does not proxy them back out.
	ErrDownloadRemote = errors.New("platform backup artifacts are stored in your S3 bucket; fetch them from there")
	// ErrUnknownSubject is returned for an unrecognized backup subject.
	ErrUnknownSubject = errors.New("unknown platform backup subject")

	// ErrVolumeExcluded is returned for a volume that must never appear in a
	// platform backup (backup volumes, tenant volumes, the registry's storage).
	ErrVolumeExcluded = errors.New("this volume cannot be included in a platform backup")
	// ErrNoPassphrase is returned when encryption is on but no passphrase is set.
	ErrNoPassphrase = errors.New("backup encryption is enabled but no backup passphrase is set")
	// ErrSetNeedsS3 is returned when a recovery point is requested without an S3
	// target. A recovery point that lives only on the host it protects is not a
	// recovery point.
	ErrSetNeedsS3 = errors.New("a platform recovery point requires an S3 target: a backup stored only on the host you are recovering from is no disaster recovery at all")

	// pg-bkup emits "<db>_YYYYMMDD_...sql.gz"; volume-bkup emits "<name>_...tar.gz". With GPG_PASSPHRASE set
	// both append ".gpg", so the artifact regexes must accept the encrypted form or the run completes with an
	// empty Filename and nothing to restore from.
	dbArtifactRe  = regexp.MustCompile(`[\w.\-]+\.sql\.gz(?:\.gpg)?`)
	volArtifactRe = regexp.MustCompile(`[\w.\-]+\.tar\.gz(?:\.gpg)?`)
)

const (
	volumeMount = "/data"

	// legacyLocalVolume held platform artifacts back when a local destination was allowed. It is no longer
	// written to — S3 is the only destination — but the name is kept so the volume is still excluded from
	// platform backups and old rows pointing at it can be recognized.
	legacyLocalVolume = "mb-platform-backups"

	// destS3 is the only destination. A backup written to a volume on the host it
	// protects cannot be read once that host is gone, which is the one situation
	// platform backup exists for.
	destS3 = "s3"

	defaultPgImage  = "jkaninda/pg-bkup:latest"
	defaultVolImage = "jkaninda/volume-bkup:latest"
)

// artifactName extracts the artifact the helper actually uploaded, taking the LAST match: with encryption on
// the tools narrate the plain dump before the encrypted one they upload, so the first match names a file that
// was never written. Encryption is read from the name, not the intent, since older helpers ignore the flag.
func artifactName(out string, re *regexp.Regexp) (name string, encrypted bool, err error) {
	return backup.ArtifactName(out, re)
}

func notEncrypted(subject, name string) {
	logger.Warn("artifact stored UNENCRYPTED despite encryption being enabled: the backup helper does not support it — upgrade the image",
		"subject", subject, "artifact", name)
}

// oneShotError builds the error for a failed helper container. The tool reports its problem on stdout and exits
// non-zero, in which case RunOneShot returns a nil error — so wrapping that nil with %w produced a message
// naming the one thing nobody needed. The container's own output is the diagnosis; carry it.
func oneShotError(action string, exit int, out string, err error) error {
	detail := strings.TrimSpace(out)
	if detail == "" {
		detail = "(no output)"
	}
	// Keep the tail: these tools print their progress first and their failure
	// last, and the row this lands in is not a log file.
	if len(detail) > 2000 {
		detail = "…" + detail[len(detail)-2000:]
	}
	if err != nil {
		return fmt.Errorf("%s could not run: %w (output: %s)", action, err, detail)
	}
	return fmt.Errorf("%s exited with code %d: %s", action, exit, detail)
}

// assertDBReachable refuses a connection that cannot possibly work from inside a helper container. The backup
// runs in its own container on the platform network, so a loopback address points at that container, not at the
// host running Miabi. Left to discover it, the tool fails with a refusal that reads like the database is down.
func (s *Service) assertDBReachable() error {
	switch strings.ToLower(strings.TrimSpace(s.db.Host)) {
	case "", "localhost", "127.0.0.1", "::1", "[::1]":
		return fmt.Errorf(
			"the control-plane database host is %q. The backup runs in its own container, where that address means "+
				"the container itself — not the machine Miabi runs on — so it can never connect. Set MIABI_DB_HOST to "+
				"an address the container can reach: the Postgres container's name when Miabi is installed as a stack "+
				"(%q, on the %s network), or host.docker.internal when Postgres runs on the host and Docker provides "+
				"that name",
			s.db.Host, "miabi-postgres", s.networkName())
	}
	return nil
}

func (s *Service) networkName() string {
	if s.network == "" {
		return "platform"
	}
	return s.network
}

// DBConn carries the resolved control-plane Postgres connection parameters the
// platform DB backup runner feeds to pg-bkup.
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

// Enqueuer schedules a platform backup to run in the background worker. Satisfied
// by worker.Producer. When unset, backups run synchronously (tests / no-redis).
type Enqueuer interface {
	EnqueuePlatformBackup(backupID uint) error
}

// Service backs up and restores the platform's own database and volumes.
type Service struct {
	repo        *repositories.PlatformBackupRepository
	sets        *repositories.PlatformBackupSetRepository
	settings    *repositories.PlatformBackupSettingsRepository
	clients     NodeDocker
	db          DBConn
	network     string // proxy network attached so pg-bkup can reach a managed DB by name
	images      ImageResolver
	enqueuer    Enqueuer
	logs        *logstore.Store
	identity    IdentitySource
	fingerprint func(label string) string
	// keyFingerprint derives the fingerprint an *arbitrary* key would produce, so a
	// key recovered from an identity envelope can be checked against a set.
	keyFingerprint func(key, label string) string
	// env is the environment-supplied configuration, which wins over the stored
	// settings row (see settings.go).
	env config.PlatformBackupConfig
	// tenants enumerates and dumps workspace data (see tenant.go). Optional.
	tenants TenantSource
	// apps stops and starts the applications using a volume around a restore.
	// Optional; without it a volume restore runs with the apps still attached.
	apps AppStopper
}

// NewService builds the platform backup service. db is the control-plane DB
// connection; network is the proxy network the DB-backup container joins so it
// can reach a Compose/managed Postgres by service name.
func NewService(repo *repositories.PlatformBackupRepository, sets *repositories.PlatformBackupSetRepository, settings *repositories.PlatformBackupSettingsRepository, clients NodeDocker, db DBConn, network string) *Service {
	return &Service{repo: repo, sets: sets, settings: settings, clients: clients, db: db, network: network}
}

// SetImageResolver wires the deployment-config resolver for the backup tool images.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

// SetLogStore wires the shared execution-log store. When set, a run's full output is externalized on
// terminal state and the DB row keeps only a bounded tail plus a reference. Platform backups have no
// workspace, so their objects live under an admin-only prefix.
func (s *Service) SetLogStore(store *logstore.Store) { s.logs = store }

// externalizeLog moves a terminal platform-backup's full output into the shared log store and trims the
// row to a bounded tail + a reference. No-op when the store is disabled or already externalized; on any
// error the full log stays in the DB tail.
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

// backupNetworks attaches the helper container to the platform network so it can
// reach the control-plane database by name.
func (s *Service) backupNetworks(ctx context.Context, dc docker.Client) ([]string, error) {
	if s.network == "" {
		return nil, nil
	}
	if _, err := dc.EnsureNetwork(ctx, s.network); err != nil {
		return nil, fmt.Errorf("attach the backup container to network %q (it reaches the database by name there): %w", s.network, err)
	}
	return []string{s.network}, nil
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

// Create records a pending platform backup and enqueues it for the background worker, returning the
// pending record immediately. With no enqueuer wired it runs synchronously. Volume backups require an S3
// target, since volume-bkup has no local destination.
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

// RunBackup executes a pending platform backup (the worker entry point),
// dispatching by subject.
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

// assertBackupable refuses a volume that must never be captured in a platform
// backup (§ DiscoverVolumes), resolving its labels from Docker so a workspace
// volume named without an "mb-" prefix is still caught.
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
	if name == models.DefaultRegistryVolume {
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
	cmd := []string{"backup", "--storage", "s3", "-d", s.db.Name}
	if st.DatabaseBackupPath != "" {
		cmd = append(cmd, "--path", st.DatabaseBackupPath)
	}
	// The helper reaches the database by name on the platform network. Failing to attach is not something to
	// shrug off and continue past: the container would then be on no network at all and could not resolve the
	// host, which surfaces far from here as an unexplained exit 1.
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
	// The archive is uploaded BY THIS CONTAINER, so it must reach the object store from inside Docker. A
	// self-hosted MinIO usually resolves only on the platform network; without joining it the upload dies on "no
	// such host" after the archive was created and encrypted, reading like a storage fault rather than networking.
	nets, err := s.backupNetworks(ctx, dc)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	out, err := s.runHelper(ctx, dc, "volume backup of "+b.VolumeName, docker.RunSpec{
		Name:     fmt.Sprintf("mb-platform-volbkup-%d", b.ID),
		Image:    image,
		Env:      append(backup.S3Env(cfg), gpg...),
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

// Restore restores a completed platform backup. It runs synchronously — the admin confirms a destructive,
// maintenance-mode operation and waits for the result. A DB restore overwrites the control-plane database
// in place; a volume restore overwrites the target volume.
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
	var nets []string
	if s.network != "" {
		if _, err := dc.EnsureNetwork(ctx, s.network); err == nil {
			nets = []string{s.network}
		}
	}

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

// Delete removes a backup record and its artifact from the bucket. With S3 as the only destination, a
// record removed without its artifact leaves a file nobody knows about, paying storage forever. A failed
// object delete is logged but does not block removing the record.
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

// Download is not supported: artifacts live in the operator's own bucket, and
// streaming gigabytes of encrypted dump back out through the control plane would
// add nothing they cannot do with their S3 client.
func (s *Service) Download(_ context.Context, _ *models.PlatformBackup) (io.ReadCloser, int64, string, error) {
	return nil, 0, "", ErrDownloadRemote
}

// Prune enforces the retention policy on platform backups: keep at most
// maxBackups most-recent, and delete any older than retentionDays. A zero bound
// is ignored. Returns the number removed.
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
		// Artifacts owned by a recovery point are retained as a unit by
		// PruneSets. Deleting one here would silently hollow out a set that still
		// reports itself as restorable.
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

// RunScheduled is the cron entry point. With an S3 target it takes a whole recovery point (identity
// envelope + database + selected volumes) and prunes by recovery point; with only a local destination it
// falls back to the legacy per-artifact backup, useful for same-host rollback but not disaster recovery.
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

// PlatformVolume is a candidate platform/system volume the admin may include in
// backups. Role is the io.miabi.role label value when the volume is infra.
type PlatformVolume struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

// DiscoverVolumes lists candidate platform/system volumes on the manager node. Three classes are excluded:
// backup volumes (which must never back themselves up), per-workspace volumes (tenant data with their own
// path), and the registry data volume (a live-blob tar restores cleanly and then fails on pull).
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

// excludedVolume reports whether a volume must never appear in a platform backup. Kept separate from
// DiscoverVolumes so the same rule can be enforced when a caller names a volume directly — a filter the
// picker honours but the API does not is not a filter.
func excludedVolume(name string, labels map[string]string) bool {
	if name == legacyLocalVolume || strings.HasPrefix(name, "mb-backups-") {
		return true
	}
	if name == models.DefaultRegistryVolume {
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
	// Close the owning recovery point too. Without this a failed item left its set "running" forever: never
	// completed, never failed, and never pruned — a recovery point stuck mid-flight in the history with no way
	// to tell whether it was still working.
	s.finalizeSet(b.SetID)
	logger.Error("platform backup failed", "backup", b.ID, "subject", b.Subject, "error", cause)
	return b
}

// volumeArchiveName sanitizes a docker volume name into a volume-bkup --name
// (the archive base name): keep alphanumerics, dash, underscore.
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
