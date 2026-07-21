// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package backup runs database backups and restores using the ecosystem
// pg-bkup / mysql-bkup tools as one-shot containers, to a per-workspace backup
// volume (local) or an S3-compatible bucket.
package backup

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/jkaninda/logger"
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

	// Backup tools name artifacts <db>_YYYYMMDD_...<ext>.gz: pg-bkup/mysql-bkup
	// emit ".sql.gz", mongodb-bkup (mongodump --archive --gzip) emits ".archive.gz".
	artifactRe = regexp.MustCompile(`[\w.\-]+\.(?:sql|archive)\.gz`)
)

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// bkupImage returns the backup tool image for an engine, taking it from the
// deployment-config catalog when a resolver is wired (else the built-in default).
func (s *Service) bkupImage(engine models.DBEngine) (string, bool) {
	var key, def string
	switch engine {
	case models.DBEnginePostgres:
		key, def = platformimage.KeyBackupPostgres, "jkaninda/pg-bkup:latest"
	case models.DBEngineMySQL, models.DBEngineMariaDB:
		key, def = platformimage.KeyBackupMySQL, "jkaninda/mysql-bkup:latest"
	case models.DBEngineMongoDB:
		key, def = platformimage.KeyBackupMongoDB, "jkaninda/mongodb-bkup:latest"
	case models.DBEngineLibSQL:
		key, def = platformimage.KeyBackupLibSQL, "jkaninda/libsql-bkup:latest"
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

// BackupAlerter receives database-backup outcomes so the alert engine can raise a
// "backup failed" alert and auto-resolve it on the next success. Kept as a local
// interface so the backup service stays decoupled from the alerting package (the
// wiring bridges it to the engine).
type BackupAlerter interface {
	BackupFailed(workspaceID, databaseID uint, dbName, errMsg string)
	BackupSucceeded(workspaceID, databaseID uint)
}

type Service struct {
	repo    *repositories.BackupRepository
	dbs     *repositories.DatabaseRepository
	clients NodeDocker
	images  ImageResolver
	ddl     DDLRunner
	logs    *logstore.Store
	alerter BackupAlerter
}

func NewService(repo *repositories.BackupRepository, dbs *repositories.DatabaseRepository, clients NodeDocker) *Service {
	return &Service{repo: repo, dbs: dbs, clients: clients}
}

// SetImageResolver wires the deployment-config resolver for backup tool images.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

// SetLogStore wires the shared execution-log store. When set, a backup run's
// full output is externalized to the store on terminal state and the DB row
// keeps only a bounded tail + a reference. nil keeps DB-tail-only.
func (s *Service) SetLogStore(store *logstore.Store) { s.logs = store }

// SetAlerter wires backup-outcome alerting (optional; nil = no alerts).
func (s *Service) SetAlerter(a BackupAlerter) { s.alerter = a }

// externalizeLog moves a terminal backup's full output into the shared log
// store and trims the row to a bounded tail + a reference. No-op when the store
// is disabled or already externalized; on any error the full log stays in the
// DB tail.
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
	// Keep the in-memory row consistent with what was persisted, so a caller
	// returning b to the API surfaces the tail + ref, not the full log.
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

// S3Env builds the S3 environment for the ecosystem backup tools (pg-bkup,
// mysql-bkup, volume-bkup), which all read the same variable names. Exported so
// the volume backup service can target the same bucket without duplicating the
// var-name contract.
func S3Env(c *S3Config) []string { return s3Env(c) }

// s3Env builds the pg-bkup / mysql-bkup S3 environment.
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

// ensureDBNetworks makes every network the instance is on exist on the node and
// returns their names, so a backup/restore helper joins the same full set the
// instance's container runs on and can reach it by name. Attaching to only the
// primary risks landing on a network the instance is not on, surfacing as
// "could not translate host name <alias>". Mirrors the database service's
// bring-up. Falls back to the gateway for legacy instances.
func ensureDBNetworks(ctx context.Context, dc docker.Client, inst *models.DatabaseInstance) ([]string, error) {
	names := inst.NetworkNames(node.AppNetwork)
	for _, n := range names {
		if _, err := dc.EnsureNetwork(ctx, n); err != nil {
			return nil, fmt.Errorf("ensure network %s: %w", n, err)
		}
	}
	return names, nil
}

// Run performs a backup of a logical database to the given destination and
// records the result. inst is the server hosting db.
func (s *Service) Run(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, trigger string, dest Destination) (*models.Backup, error) {
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
		Status: models.BackupRunning, Trigger: trigger, Destination: dest.Type, StartedAt: &now,
	}
	if err := s.repo.Create(b); err != nil {
		return nil, err
	}

	env, err := s.connEnv(inst, db)
	if err != nil {
		return s.fail(b, err), nil
	}
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
		return s.fail(b, fmt.Errorf("backup exited with code %d: %w", exit, err)), nil
	}
	if m := artifactRe.FindString(out); m != "" {
		b.Filename = m
	}
	fin := time.Now()
	b.Status = models.BackupCompleted
	b.FinishedAt = &fin
	_ = s.repo.Update(b)
	s.externalizeLog(b)
	if s.alerter != nil {
		s.alerter.BackupSucceeded(b.WorkspaceID, b.DatabaseID)
	}
	logger.Info("backup completed", "database", db.ID, "destination", dest.Type, "file", b.Filename)
	return b, nil
}

// helperImage is the small image used to land an uploaded dump on the volume
// (deployment-config catalog, else busybox).
func (s *Service) helperImage() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyHelper); r != "" {
			return r
		}
	}
	return "busybox:1.36"
}

// uploadName builds a unique artifact name for an uploaded dump, preserving a
// recognized extension (the backup tool keys off it).
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

// RestoreUpload streams an uploaded dump onto the workspace backup volume and
// restores the database from it (force drops & recreates first). The temp
// artifact is removed afterward (best-effort).
func (s *Service) RestoreUpload(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, origName string, content io.Reader, size int64, force bool) error {
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
	name := uploadName(origName)
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

// DDLRunner recreates a logical database for a force restore. Implemented by the
// database service; injected after construction (optional).
type DDLRunner interface {
	RecreateDatabase(ctx context.Context, inst *models.DatabaseInstance, db *models.Database) error
}

// SetDDLRunner wires the database service used to drop+recreate a database on a
// force restore.
func (s *Service) SetDDLRunner(r DDLRunner) { s.ddl = r }

// RestoreSpec describes a restore source. Filename is the artifact on the local
// backup volume or the S3 key; Force drops & recreates the database first.
type RestoreSpec struct {
	Filename    string
	Destination string // "local" | "s3"
	VolumeName  string
	S3          *S3Config
	S3Path      string // remote folder prefix the artifact was stored under
	Force       bool
}

// RestoreFromBackup restores a logical database from a stored backup record. For
// an S3 backup, dest.S3 must supply the bucket credentials again.
func (s *Service) RestoreFromBackup(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, b *models.Backup, dest Destination, force bool) error {
	if b.Filename == "" {
		return ErrNoBackupFile
	}
	return s.Restore(ctx, inst, db, RestoreSpec{
		Filename:    b.Filename,
		Destination: b.Destination,
		VolumeName:  b.VolumeName,
		S3:          dest.S3,
		S3Path:      b.S3Path,
		Force:       force,
	})
}

// Restore restores a logical database from the given source. When spec.Force is
// set the database is dropped & recreated first (clean slate).
func (s *Service) Restore(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, spec RestoreSpec) error {
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
	if err != nil || exit != 0 {
		return fmt.Errorf("restore exited with code %d: %s", exit, out)
	}
	return nil
}

func (s *Service) List(databaseID uint) ([]models.Backup, error) {
	return s.repo.ListByDatabase(databaseID)
}

// Delete removes a backup record and, for local backups, its artifact file from
// the workspace backup volume (best-effort). S3 artifacts are left in place.
func (s *Service) Delete(ctx context.Context, b *models.Backup) error {
	if b.Destination == "local" && b.VolumeName != "" && b.Filename != "" {
		dc, derr := s.clients.For(b.ServerID)
		if image, ok := s.bkupImage(b.Engine); ok && derr == nil {
			// The backup tool images are alpine-based, so /bin/sh is available.
			_, out, err := dc.RunOneShot(ctx, docker.RunSpec{
				Name:       fmt.Sprintf("mb-backup-rm-%d", b.ID),
				Image:      image,
				Entrypoint: []string{"/bin/sh", "-c"},
				Cmd:        []string{"rm -f " + backupMount + "/" + b.Filename},
				Mounts:     map[string]string{b.VolumeName: backupMount},
			})
			if err != nil {
				logger.Error("remove backup artifact", "backup", b.ID, "error", err, "out", out)
			}
		}
	}
	return s.repo.Delete(b.ID)
}

// Prune enforces a retention policy on a database's backups: keep at most
// maxBackups most-recent, and delete any older than retentionDays. A zero bound
// is ignored. Returns the number of backups removed.
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
	for i := range backups {
		b := &backups[i]
		overCount := maxBackups > 0 && i >= maxBackups
		tooOld := retentionDays > 0 && b.CreatedAt.Before(cutoff)
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

// Download streams a local backup artifact. The returned reader must be closed
// by the caller. S3 backups are not downloadable through Miabi.
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
		// libSQL authenticates with a JWT over HTTP, not a username/password. The
		// libsql-bkup tool reads DB_URL + LIBSQL_AUTH_TOKEN (DB_HOST/DB_PORT/DB_NAME
		// kept for parity with the other tools); `pass` is the client token.
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

func (s *Service) fail(b *models.Backup, cause error) *models.Backup {
	fin := time.Now()
	b.Status = models.BackupFailed
	b.Error = cause.Error()
	b.FinishedAt = &fin
	_ = s.repo.Update(b)
	s.externalizeLog(b)
	if s.alerter != nil {
		name := ""
		if db, err := s.dbs.FindByID(b.DatabaseID); err == nil && db != nil {
			name = db.Name
		}
		s.alerter.BackupFailed(b.WorkspaceID, b.DatabaseID, name, cause.Error())
	}
	logger.Error("backup failed", "database", b.DatabaseID, "error", cause)
	return b
}
