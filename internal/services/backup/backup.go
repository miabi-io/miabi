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

// Package backup runs database backups and restores with the *-bkup tools as one-shot containers.
package backup

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dbenvelope"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrUnsupportedEngine = errors.New("backups are not supported for this engine yet")
	ErrNoBackupFile      = errors.New("backup has no artifact to restore")
	ErrDownloadRemote    = errors.New("download is only available for local backups; fetch s3 backups from your bucket")
)

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

func (s *Service) bkupImage(engine models.DBEngine) (string, bool) {
	var key, def string
	switch engine {
	case models.DBEnginePostgres:
		key, def = platformimage.KeyBackupPostgres, platformimage.DefaultPgBkup
	case models.DBEngineMySQL, models.DBEngineMariaDB:
		key, def = platformimage.KeyBackupMySQL, platformimage.DefaultMySQLBkup
	case models.DBEngineMongoDB:
		key, def = platformimage.KeyBackupMongoDB, platformimage.DefaultMongoDBBkup
	case models.DBEngineLibSQL:
		key, def = platformimage.KeyBackupLibSQL, platformimage.DefaultLibSQLBkup
	default:
		return "", false
	}
	if s.images != nil {
		if r := s.images.Ref(key); r != "" {
			return r, true
		}
	}
	return def, true
}

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

// Separate set methods: sharing the per-database pair let a set and database with equal ids share a dedup key.
type BackupAlerter interface {
	BackupFailed(workspaceID, databaseID uint, dbName, errMsg string)
	BackupSucceeded(workspaceID, databaseID uint)
	BackupSetFailed(workspaceID, instanceID uint, instanceName, ref, errMsg string)
	BackupSetSucceeded(workspaceID, instanceID uint)
}

// BackupReport is one finished scheduled run — a single database's, or a whole recovery point's.
type BackupReport struct {
	// Subject is the instance name for a set, the logical database's name otherwise.
	Subject string
	// Ref names the recovery point. Empty for a single-database backup.
	Ref  string
	Link string
	OK   bool
	// Databases is how many artifacts a set carries. Zero for a single-database backup.
	Databases int
	SizeBytes int64
	Err       string
}

// Scheduled runs only: nobody watches a 03:00 run, while a manual one reports in the row its operator is viewing.
type BackupReporter interface {
	ScheduledBackupFinished(workspaceID uint, rep BackupReport)
}

// scheduledTrigger is the Trigger value the cron manager stamps on a run.
const scheduledTrigger = "scheduled"

// The subject is the instance so one timeline covers it; the logical database rides in metadata.
type EventRecorder interface {
	EmitDatabase(workspaceID, databaseID uint, name string, t models.AppEventType, sev models.AppEventSeverity, message string, meta map[string]string, actorID *uint)
}

type Service struct {
	repo     *repositories.BackupRepository
	dbs      *repositories.DatabaseRepository
	clients  NodeDocker
	images   ImageResolver
	ddl      DDLRunner
	logs     *logstore.Store
	alerter  BackupAlerter
	reporter BackupReporter
	events   EventRecorder
	sets     *repositories.DatabaseBackupSetRepository
}

func NewService(repo *repositories.BackupRepository, dbs *repositories.DatabaseRepository, clients NodeDocker) *Service {
	return &Service{repo: repo, dbs: dbs, clients: clients}
}

// SetEventRecorder wires the instance timeline recorder (nil-safe: unset means no events).
func (s *Service) SetEventRecorder(r EventRecorder) { s.events = r }

// emit is best-effort and nil-safe: recording must never break a backup.
func (s *Service) emit(workspaceID, instanceID uint, instanceName string, t models.AppEventType, sev models.AppEventSeverity, msg string, meta map[string]string) {
	if s.events == nil || instanceID == 0 {
		return
	}
	s.events.EmitDatabase(workspaceID, instanceID, instanceName, t, sev, msg, meta, nil)
}

// SetImageResolver wires the deployment-config resolver for backup tool images.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

// When set, full output goes to the store on terminal state and the row keeps a bounded tail.
func (s *Service) SetLogStore(store *logstore.Store) { s.logs = store }

// SetAlerter wires backup-outcome alerting (optional; nil = no alerts).
func (s *Service) SetAlerter(a BackupAlerter) { s.alerter = a }

// SetReporter wires scheduled-run inbox reporting (optional; nil = no inbox items).
func (s *Service) SetReporter(r BackupReporter) { s.reporter = r }

// Trigger-gated here so no call site has to remember which runs are worth reporting.
func (s *Service) report(workspaceID uint, trigger string, rep BackupReport) {
	if s.reporter == nil || trigger != scheduledTrigger {
		return
	}
	s.reporter.ScheduledBackupFinished(workspaceID, rep)
}

// On any error the full log stays in the DB tail.
func (s *Service) externalizeLog(b *models.Backup) {
	if !s.logs.Enabled() || b.LogRef != "" {
		return
	}
	ref := logstore.BackupRef(b.WorkspaceID, b.ID)
	res, err := s.logs.Externalize(ref, b.Logs)
	if err != nil {
		logger.Error("log store: externalize backup log failed", "backup", b.ID, "error", err)
		return
	}
	if err := s.repo.SetLogMeta(b.ID, res.Ref, res.Tail, res.Bytes, res.Lines, res.Truncated); err != nil {
		logger.Error("log store: record backup log ref failed", "backup", b.ID, "error", err)
		return
	}
	// Keep the in-memory row consistent so the API surfaces the tail and ref, not the full log.
	b.LogRef, b.Logs = res.Ref, res.Tail
	b.LogBytes, b.LogLines, b.LogTruncated = res.Bytes, res.Lines, res.Truncated
}

const backupMount = "/backup"

// S3Config targets an S3-compatible bucket (endpoint empty => AWS).
type S3Config struct {
	Endpoint       string
	Bucket         string
	Region         string
	AccessKey      string
	SecretKey      string
	Path           string
	UseSSL         bool
	ForcePathStyle bool
}

// Destination selects where a backup is stored.
type Destination struct {
	Type string // "local" | "s3"
	S3   *S3Config
	// The *-bkup tools write "<name>.gpg" when set and decrypt transparently on restore.
	GPGPassphrase string
	// Only the matching identity can read it back, so the platform can take backups it cannot open.
	AgeRecipients string
	// AgeIdentity decrypts ".age" artifacts on restore and for the post-backup verification.
	AgeIdentity string
	// SkipVerify turns off the full read-back of each artifact after it is written.
	SkipVerify bool
}

func workspaceBackupVolume(workspaceID uint) string {
	return fmt.Sprintf("mb-backups-%d", workspaceID)
}

func boolEnv(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// S3Env is exported so the volume backup service shares the tools' env var contract.
func S3Env(c *S3Config) []string { return s3Env(c) }

func s3Env(c *S3Config) []string {
	return []string{
		"AWS_S3_ENDPOINT=" + c.Endpoint,
		"AWS_S3_BUCKET_NAME=" + c.Bucket,
		"AWS_REGION=" + c.Region,
		"AWS_ACCESS_KEY=" + c.AccessKey,
		"AWS_SECRET_KEY=" + c.SecretKey,
		"AWS_DISABLE_SSL=" + boolEnv(!c.UseSSL),
		"AWS_FORCE_PATH_STYLE=" + boolEnv(c.ForcePathStyle),
	}
}

// Attaching only the primary network risks one the instance is not on; legacy instances use the gateway network.
func ensureDBNetworks(ctx context.Context, dc docker.Client, inst *models.DatabaseInstance) ([]string, error) {
	names := inst.NetworkNames(node.AppNetwork)
	for _, n := range names {
		if inst.SwarmScoped(n) {
			continue
		}
		if _, err := dc.EnsureNetwork(ctx, n); err != nil {
			return nil, fmt.Errorf("ensure network %s: %w", n, err)
		}
	}
	return names, nil
}

// A struct so the two adjacent free-text values can't be passed the wrong way round.
type RunOptions struct {
	Trigger string // manual | scheduled | upgrade | bundle | platform-dr
	Comment string
	// Nil for an ordinary single-database backup.
	SetID *uint
}

func (s *Service) Run(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, opts RunOptions, dest Destination) (*models.Backup, error) {
	image, ok := s.bkupImage(inst.Engine)
	if !ok {
		return nil, ErrUnsupportedEngine
	}
	if dest.Type == "" {
		dest.Type = "local"
	}
	dc, err := s.clients.For(inst.ServerID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	b := &models.Backup{
		WorkspaceID: db.WorkspaceID, DatabaseID: db.ID, Engine: inst.Engine, ServerID: inst.ServerID,
		Status: models.BackupRunning, Trigger: opts.Trigger, Destination: dest.Type, StartedAt: &now,
		Version: inst.Version, Comment: opts.Comment, SetID: opts.SetID,
	}
	if err := s.repo.Create(b); err != nil {
		return nil, err
	}

	env, err := s.connEnv(inst, db)
	if err != nil {
		return s.fail(b, err), nil
	}
	env = append(env, dest.EncryptionEnv()...)
	env = append(env, IndexEnv(inst.Name))
	cmd := []string{"backup", "-d", db.Name}
	var mounts map[string]string

	if dest.Type == "s3" {
		if dest.S3 == nil {
			return s.fail(b, fmt.Errorf("s3 destination requires configuration")), nil
		}
		env = append(env, s3Env(dest.S3)...)
		cmd = []string{"backup", "--storage", "s3", "-d", db.Name}
		if dest.S3.Path != "" {
			cmd = append(cmd, "--path", dest.S3.Path)
		}
		b.S3Bucket = dest.S3.Bucket
		b.S3Path = dest.S3.Path
	} else {
		volume := workspaceBackupVolume(db.WorkspaceID)
		if _, err := dc.CreateVolume(ctx, volume, map[string]string{docker.LabelWorkspace: fmt.Sprint(db.WorkspaceID)}, 0); err != nil {
			return s.fail(b, fmt.Errorf("create backup volume: %w", err)), nil
		}
		b.VolumeName = volume
		mounts = map[string]string{volume: backupMount}
	}

	if err := dc.PullImage(ctx, image, nil); err != nil {
		return s.fail(b, fmt.Errorf("pull backup image: %w", err)), nil
	}

	nets, err := ensureDBNetworks(ctx, dc, inst)
	if err != nil {
		return s.fail(b, err), nil
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:     fmt.Sprintf("mb-backup-%d", b.ID),
		Image:    image,
		Env:      env,
		Cmd:      cmd,
		Networks: nets,
		Mounts:   mounts,
	})
	b.Logs = out
	if err != nil || exit != 0 {
		// RunOneShot returns nil on a non-zero exit, so the tool's own output must carry the failure.
		return s.fail(b, oneShotFailure(exit, out, err)), nil
	}
	name, encrypted, nameErr := ArtifactName(out, DBArtifactRe)
	if nameErr != nil {
		return s.fail(b, nameErr), nil
	}
	b.Filename = name
	b.Encrypted = encrypted
	if size := ArtifactSize(out); size > 0 {
		b.SizeBytes = size
	}
	if dest.Encrypting() && !encrypted {
		logger.Warn("backup stored UNENCRYPTED despite encryption being configured: the backup tool does not support it — upgrade the image",
			"database", db.Name, "artifact", name)
	}
	b.Checksum = ArtifactSHA256(out)
	if !dest.SkipVerify {
		vcmd := []string{"verify", "-d", db.Name, "-f", name}
		if dest.Type == "s3" {
			vcmd = []string{"verify", "--storage", "s3", "-d", db.Name, "-f", name}
			if dest.S3.Path != "" {
				vcmd = append(vcmd, "--path", dest.S3.Path)
			}
		}
		venv, _ := s.connEnv(inst, db)
		if dest.Type == "s3" {
			venv = append(venv, s3Env(dest.S3)...)
		}
		venv = append(venv, IndexEnv(inst.Name))
		s.verifyBackup(ctx, dc, image, inst, b, dest, venv, vcmd, nets, mounts)
	}
	fin := time.Now()
	b.Status = models.BackupCompleted
	b.FinishedAt = &fin
	_ = s.repo.Update(b)
	s.externalizeLog(b)
	if s.alerter != nil {
		s.alerter.BackupSucceeded(b.WorkspaceID, b.DatabaseID)
	}
	// The set posts one item for all its databases; reporting here too would give N+1 notices.
	if opts.SetID == nil {
		s.report(b.WorkspaceID, opts.Trigger, BackupReport{
			Subject: db.Name, Link: fmt.Sprintf("/databases/%d", inst.ID),
			OK: true, SizeBytes: b.SizeBytes,
		})
	}
	s.emit(b.WorkspaceID, inst.ID, inst.Name, models.EventDatabaseBackupSucceeded, models.SeverityInfo,
		fmt.Sprintf("Backup #%d of %q completed", b.Number, db.Name),
		map[string]string{"database": db.Name, "backup": fmt.Sprint(b.Number), "trigger": opts.Trigger, "destination": dest.Type})
	logger.Info("backup completed", "database", db.ID, "destination", dest.Type, "file", b.Filename)
	return b, nil
}

// A verification failure doesn't undo the backup but is surfaced as a warning on the instance.
func (s *Service) verifyBackup(ctx context.Context, dc docker.Client, image string, inst *models.DatabaseInstance, b *models.Backup,
	dest Destination, env, cmd, nets []string, mounts map[string]string) {
	keys, err := RestoreKeyEnv(b.Filename, dest.GPGPassphrase, dest.AgeIdentity)
	if err != nil {
		// age recipients without a stored identity: the platform cannot read its own backups, by design.
		logger.Info("backup not verified: no age identity to read it back", "backup", b.ID)
		return
	}
	exit, out, rerr := dc.RunOneShot(ctx, docker.RunSpec{
		Name:     fmt.Sprintf("mb-backup-verify-%d", b.ID),
		Image:    image,
		Env:      append(env, keys...),
		Cmd:      cmd,
		Networks: nets,
		Mounts:   mounts,
	})
	status, reason := VerifyOutcome(exit, out, rerr)
	if status == "" {
		return
	}
	now := time.Now()
	b.VerifiedAt, b.VerifyStatus, b.VerifyError = &now, status, reason
	if status == VerifyFailed {
		logger.Warn("backup failed verification", "backup", b.ID, "file", b.Filename, "error", reason)
		s.emit(b.WorkspaceID, inst.ID, inst.Name, models.EventDatabaseBackupFailed, models.SeverityWarning,
			fmt.Sprintf("Backup #%d failed verification: %s", b.Number, reason),
			map[string]string{"backup": fmt.Sprint(b.Number), "check": "verify"})
	}
}

func (s *Service) helperImage() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyHelper); r != "" {
			return r
		}
	}
	return "busybox:1.36"
}

// The backup tool keys off the extension, so a recognized one is preserved.
func uploadName(orig string) string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	name := "mb-upload-" + hex.EncodeToString(b)
	for _, ext := range []string{".sql.gz", ".sql", ".dump"} {
		if strings.HasSuffix(strings.ToLower(orig), ext) {
			return name + ext
		}
	}
	return name + ".sql.gz"
}

func (s *Service) RestoreUpload(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, origName string, content io.Reader, size int64, force bool) error {
	return s.restoreStream(ctx, inst, db, uploadName(origName), content, size, force)
}

// RestoreFile keeps the name so the tool reads the format from its extension.
func (s *Service) RestoreFile(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, file string, content io.Reader, size int64) error {
	b := make([]byte, 4)
	_, _ = crand.Read(b)
	return s.restoreStream(ctx, inst, db, "mb-mig-"+hex.EncodeToString(b)+"-"+filepath.Base(file), content, size, false)
}

func (s *Service) restoreStream(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, name string, content io.Reader, size int64, force bool) error {
	if _, ok := s.bkupImage(inst.Engine); !ok {
		return ErrUnsupportedEngine
	}
	dc, err := s.clients.For(inst.ServerID)
	if err != nil {
		return err
	}
	volume := workspaceBackupVolume(db.WorkspaceID)
	if _, err := dc.CreateVolume(ctx, volume, map[string]string{docker.LabelWorkspace: fmt.Sprint(db.WorkspaceID)}, 0); err != nil {
		return fmt.Errorf("create backup volume: %w", err)
	}
	helper := s.helperImage()
	if err := dc.PullImage(ctx, helper, nil); err != nil {
		return fmt.Errorf("pull helper image: %w", err)
	}
	if err := dc.CopyToVolume(ctx, volume, helper, name, content, size); err != nil {
		return fmt.Errorf("upload dump: %w", err)
	}
	defer func() {
		// Best-effort cleanup of the temp upload artifact.
		_, _, _ = dc.RunOneShot(context.Background(), docker.RunSpec{
			Name:   "mb-rmupload-" + name,
			Image:  helper,
			Cmd:    []string{"sh", "-c", "rm -f " + backupMount + "/" + name},
			Mounts: map[string]string{volume: backupMount},
		})
	}()
	return s.Restore(ctx, inst, db, RestoreSpec{Filename: name, Destination: "local", VolumeName: volume, Force: force})
}

type DDLRunner interface {
	RecreateDatabase(ctx context.Context, inst *models.DatabaseInstance, db *models.Database) error
}

func (s *Service) SetDDLRunner(r DDLRunner) { s.ddl = r }

type RestoreSpec struct {
	Filename    string
	Destination string // "local" | "s3"
	VolumeName  string
	S3          *S3Config
	S3Path      string // remote folder prefix the artifact was stored under
	Force       bool
	// GPGPassphrase decrypts a ".gpg" artifact (see Destination.GPGPassphrase).
	GPGPassphrase string
	// AgeIdentity decrypts a ".age" artifact (see Destination.AgeIdentity).
	AgeIdentity string
}

// A set member is sealed with the set's data key; the workspace passphrase only opens the envelope.
func (s *Service) artifactPassphrase(b *models.Backup, workspacePassphrase string) (string, error) {
	if b.SetID == nil || s.sets == nil || workspacePassphrase == "" {
		return workspacePassphrase, nil
	}
	set, err := s.sets.FindByID(*b.SetID)
	if err != nil || set.Envelope == "" {
		// Sets from before envelopes existed used the passphrase directly.
		return workspacePassphrase, nil
	}
	dataKey, err := dbenvelope.Open(set.Envelope, workspacePassphrase)
	if err != nil {
		return "", fmt.Errorf("recovery point %s: %w", set.Ref, err)
	}
	return dataKey, nil
}

// A newer dump doesn't load into an older engine, and on a force run the database is already dropped.
var ErrRestoreVersionTooNew = errors.New("the backup was taken from a newer engine than this instance runs")

// Kept local so the backup service does not depend on the database service.
func majorVersion(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	head := v
	if i := strings.IndexAny(v, ".-+ "); i >= 0 {
		head = v[:i]
	}
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Only backwards is refused; unparseable versions don't block, as refusing would be worse than trying.
func checkRestoreVersion(instVersion, backupVersion string) error {
	from, okFrom := majorVersion(backupVersion)
	to, okTo := majorVersion(instVersion)
	if !okFrom || !okTo || from <= to {
		return nil
	}
	return fmt.Errorf("%w: %s vs %s", ErrRestoreVersionTooNew, backupVersion, instVersion)
}

// The .gpg/.age suffix the tools append is the record of whether this backup needs a passphrase.
func passphraseFor(filename, pass string) string {
	if strings.HasSuffix(filename, ".gpg") {
		return pass
	}
	return ""
}

// For an S3 backup, dest.S3 must supply the bucket credentials again.
func (s *Service) RestoreFromBackup(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, b *models.Backup, dest Destination, force, allowVersionMismatch bool) error {
	if b.Filename == "" {
		return ErrNoBackupFile
	}
	if !allowVersionMismatch {
		if err := checkRestoreVersion(inst.Version, b.Version); err != nil {
			return err
		}
	}
	pass, err := s.artifactPassphrase(b, dest.GPGPassphrase)
	if err != nil {
		return err
	}
	return s.Restore(ctx, inst, db, RestoreSpec{
		Filename:    b.Filename,
		Destination: b.Destination,
		VolumeName:  b.VolumeName,
		S3:          dest.S3,
		S3Path:      b.S3Path,
		Force:       force,
		// Keyed off the artifact: a backup taken before a passphrase was set is cleartext.
		GPGPassphrase: passphraseFor(b.Filename, pass),
		AgeIdentity:   dest.AgeIdentity,
	})
}

// Wraps restore so every early return still reports to the instance timeline.
func (s *Service) Restore(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, spec RestoreSpec) error {
	err := s.restore(ctx, inst, db, spec)
	meta := map[string]string{"database": db.Name, "file": spec.Filename, "source": spec.Destination}
	if spec.Force {
		meta["force"] = "true"
	}
	if err != nil {
		s.emit(inst.WorkspaceID, inst.ID, inst.Name, models.EventDatabaseRestoreFailed, models.SeverityError,
			fmt.Sprintf("Restore of %q failed: %s", db.Name, err.Error()), meta)
		return err
	}
	s.emit(inst.WorkspaceID, inst.ID, inst.Name, models.EventDatabaseRestoreSucceeded, models.SeverityInfo,
		fmt.Sprintf("Restored %q from %s", db.Name, spec.Filename), meta)
	return nil
}

func (s *Service) restore(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, spec RestoreSpec) error {
	image, ok := s.bkupImage(inst.Engine)
	if !ok {
		return ErrUnsupportedEngine
	}
	if spec.Filename == "" {
		return ErrNoBackupFile
	}
	dc, err := s.clients.For(inst.ServerID)
	if err != nil {
		return err
	}
	if spec.Force {
		if s.ddl == nil {
			return fmt.Errorf("force restore is unavailable")
		}
		if err := s.ddl.RecreateDatabase(ctx, inst, db); err != nil {
			return fmt.Errorf("force: %w", err)
		}
	}
	env, err := s.connEnv(inst, db)
	if err != nil {
		return err
	}
	keys, err := RestoreKeyEnv(spec.Filename, spec.GPGPassphrase, spec.AgeIdentity)
	if err != nil {
		return err
	}
	env = append(env, keys...)
	cmd := []string{"restore", "-d", db.Name, "-f", spec.Filename}
	var mounts map[string]string

	if spec.Destination == "s3" {
		if spec.S3 == nil {
			return fmt.Errorf("restoring an s3 backup requires s3 configuration")
		}
		env = append(env, s3Env(spec.S3)...)
		cmd = []string{"restore", "--storage", "s3", "-d", db.Name, "-f", spec.Filename}
		if spec.S3Path != "" {
			cmd = append(cmd, "--path", spec.S3Path)
		}
	} else {
		mounts = map[string]string{spec.VolumeName: backupMount}
	}

	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull backup image: %w", err)
	}

	nets, err := ensureDBNetworks(ctx, dc, inst)
	if err != nil {
		return err
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:     fmt.Sprintf("mb-restore-%d-%d", db.ID, time.Now().UnixNano()%100000),
		Image:    image,
		Env:      env,
		Cmd:      cmd,
		Networks: nets,
		Mounts:   mounts,
	})
	if err != nil {
		return fmt.Errorf("restore: %w: %s", err, out)
	}
	if exit != 0 {
		return fmt.Errorf("restore exited with code %d: %s", exit, out)
	}
	return nil
}

func (s *Service) List(databaseID uint) ([]models.Backup, error) {
	return s.repo.ListByDatabase(databaseID)
}

// Editable after the fact because scheduled backups run unattended and their purpose emerges later.
func (s *Service) Annotate(b *models.Backup, comment *string, pinned *bool) error {
	if comment != nil {
		b.Comment = strings.TrimSpace(*comment)
	}
	if pinned != nil {
		b.Pinned = *pinned
	}
	return s.repo.Update(b)
}

// S3 artifacts are left in place.
func (s *Service) Delete(ctx context.Context, b *models.Backup) error {
	if b.Destination == "local" && b.VolumeName != "" && b.Filename != "" {
		dc, derr := s.clients.For(b.ServerID)
		if image, ok := s.bkupImage(b.Engine); ok && derr == nil {
			s.deleteLocalArtifact(ctx, dc, image, b)
		}
	}
	return s.repo.Delete(b.ID)
}

// The delete command also drops the sidecar and index entry; older images fall back to the shell.
func (s *Service) deleteLocalArtifact(ctx context.Context, dc docker.Client, image string, b *models.Backup) {
	env := []string{}
	cmd := []string{"delete", "-f", b.Filename}
	if db, err := s.dbs.FindDatabaseByID(b.DatabaseID); err == nil && db != nil {
		cmd = append(cmd, "-d", db.Name)
		if inst, err := s.dbs.FindByID(db.InstanceID); err == nil && inst != nil {
			env = append(env, IndexEnv(inst.Name))
		}
	}
	mounts := map[string]string{b.VolumeName: backupMount}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name: fmt.Sprintf("mb-backup-rm-%d", b.ID), Image: image, Env: env, Cmd: cmd, Mounts: mounts,
	})
	if err == nil && exit == 0 {
		return
	}
	if err == nil && !strings.Contains(out, "unknown command") {
		logger.Error("delete backup artifact", "backup", b.ID, "exit", exit, "out", out)
		return
	}
	_, out, err = dc.RunOneShot(ctx, docker.RunSpec{
		Name:       fmt.Sprintf("mb-backup-rmsh-%d", b.ID),
		Image:      image,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"rm -f " + backupMount + "/" + b.Filename},
		Mounts:     mounts,
	})
	if err != nil {
		logger.Error("remove backup artifact", "backup", b.ID, "error", err, "out", out)
	}
}

// Pinned backups are never pruned and don't occupy a maxBackups slot.
func (s *Service) Prune(ctx context.Context, databaseID uint, maxBackups, retentionDays int) (int, error) {
	if maxBackups <= 0 && retentionDays <= 0 {
		return 0, nil
	}
	backups, err := s.repo.ListByDatabase(databaseID) // newest-first
	if err != nil {
		return 0, err
	}
	var cutoff time.Time
	if retentionDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -retentionDays)
	}
	removed := 0
	rank := 0
	for i := range backups {
		b := &backups[i]
		if b.Pinned {
			continue
		}
		overCount := maxBackups > 0 && rank >= maxBackups
		tooOld := retentionDays > 0 && b.CreatedAt.Before(cutoff)
		rank++
		if overCount || tooOld {
			if err := s.Delete(ctx, b); err != nil {
				logger.Error("prune backup", "backup", b.ID, "error", err)
				continue
			}
			removed++
		}
	}
	if removed > 0 {
		logger.Info("pruned backups", "database", databaseID, "removed", removed)
	}
	return removed, nil
}

// S3 backups are not downloadable through Miabi.
func (s *Service) Download(ctx context.Context, b *models.Backup) (io.ReadCloser, int64, string, error) {
	if b.Destination != "local" {
		return nil, 0, "", ErrDownloadRemote
	}
	if b.Filename == "" || b.VolumeName == "" {
		return nil, 0, "", ErrNoBackupFile
	}
	image, ok := s.bkupImage(b.Engine)
	if !ok {
		return nil, 0, "", ErrUnsupportedEngine
	}
	dc, err := s.clients.For(b.ServerID)
	if err != nil {
		return nil, 0, "", err
	}
	rc, size, err := dc.CopyFileFromVolume(ctx, b.VolumeName, image, b.Filename)
	if err != nil {
		return nil, 0, "", err
	}
	return rc, size, b.Filename, nil
}

func (s *Service) Get(workspaceID, id uint) (*models.Backup, error) {
	return s.repo.FindInWorkspace(workspaceID, id)
}

func (s *Service) connEnv(inst *models.DatabaseInstance, db *models.Database) ([]string, error) {
	pass, err := crypto.Decrypt(db.PasswordEnc)
	if err != nil {
		return nil, err
	}
	if inst.Engine == models.DBEngineLibSQL {
		// libSQL authenticates with a JWT, not a password; pass is the client token.
		return []string{
			"DB_HOST=" + inst.Host,
			fmt.Sprintf("DB_PORT=%d", inst.Port),
			"DB_NAME=" + db.Name,
			fmt.Sprintf("DB_URL=http://%s:%d", inst.Host, inst.Port),
			"LIBSQL_AUTH_TOKEN=" + pass,
		}, nil
	}
	return []string{
		"DB_HOST=" + inst.Host,
		fmt.Sprintf("DB_PORT=%d", inst.Port),
		"DB_NAME=" + db.Name,
		"DB_USERNAME=" + db.Username,
		"DB_PASSWORD=" + pass,
	}, nil
}

// Takes the last match: with encryption on, the tools narrate the plain dump before the uploaded encrypted one.
func ArtifactName(out string, re *regexp.Regexp) (name string, encrypted bool, err error) {
	matches := re.FindAllString(out, -1)
	if len(matches) == 0 {
		return "", false, fmt.Errorf("the backup reported success but named no artifact; output: %s", strings.TrimSpace(out))
	}
	name = matches[len(matches)-1]
	return name, IsEncryptedArtifact(name), nil
}

// oneShotFailure formats a backup helper's failure around its output.
func oneShotFailure(exit int, out string, err error) error {
	detail := strings.TrimSpace(out)
	if detail == "" {
		detail = "(no output)"
	}
	if len(detail) > 2000 {
		detail = "…" + detail[len(detail)-2000:]
	}
	if err != nil {
		return fmt.Errorf("backup could not run: %w (output: %s)", err, detail)
	}
	return fmt.Errorf("backup exited with code %d: %s", exit, detail)
}

func (s *Service) fail(b *models.Backup, cause error) *models.Backup {
	fin := time.Now()
	b.Status = models.BackupFailed
	b.Error = cause.Error()
	b.FinishedAt = &fin
	_ = s.repo.Update(b)
	s.externalizeLog(b)
	var (
		name       string
		instanceID uint
	)
	// FindByID resolves instance ids and would name an unrelated instance sharing the number.
	if db, err := s.dbs.FindDatabaseByID(b.DatabaseID); err == nil && db != nil {
		name, instanceID = db.Name, db.InstanceID
	}
	if s.alerter != nil {
		s.alerter.BackupFailed(b.WorkspaceID, b.DatabaseID, name, cause.Error())
	}
	var instanceName string
	if instanceID != 0 {
		if inst, err := s.dbs.FindByID(instanceID); err == nil && inst != nil {
			instanceName = inst.Name
		}
	}
	if b.SetID == nil {
		s.report(b.WorkspaceID, b.Trigger, BackupReport{
			Subject: name, Link: fmt.Sprintf("/databases/%d", instanceID),
			OK: false, Err: cause.Error(),
		})
	}
	s.emit(b.WorkspaceID, instanceID, instanceName, models.EventDatabaseBackupFailed, models.SeverityError,
		fmt.Sprintf("Backup #%d of %q failed: %s", b.Number, name, cause.Error()),
		map[string]string{"database": name, "backup": fmt.Sprint(b.Number), "trigger": b.Trigger})
	logger.Error("backup failed", "database", b.DatabaseID, "error", cause)
	return b
}
