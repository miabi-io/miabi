// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	cronpkg "github.com/miabi-io/miabi/internal/cron"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/middlewares"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type BackupHandler struct {
	svc             *backup.Service
	dbs             *repositories.DatabaseRepository
	repo            *repositories.BackupRepository
	settings        *backupsettings.Service
	cron            *cronpkg.Manager
	audit           *audit.Logger
	maxRestoreBytes int64
	logs            *logstore.Store
}

func NewBackupHandler(svc *backup.Service, dbs *repositories.DatabaseRepository, repo *repositories.BackupRepository, settings *backupsettings.Service, cron *cronpkg.Manager, auditLog *audit.Logger, maxRestoreMB int) *BackupHandler {
	return &BackupHandler{svc: svc, dbs: dbs, repo: repo, settings: settings, cron: cron, audit: auditLog, maxRestoreBytes: int64(maxRestoreMB) * 1024 * 1024}
}

// SetLogStore wires the shared execution-log store so a backup run's full log
// can be downloaded from the store (falling back to the DB tail). nil keeps
// tail-only.
func (h *BackupHandler) SetLogStore(s *logstore.Store) { h.logs = s }

// LogsDownload streams a backup run's full log as a file download.
func (h *BackupHandler) LogsDownload(c *okapi.Context) error {
	db, err := h.loadDB(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	b, err := h.loadBackup(c, db.WorkspaceID)
	if err != nil {
		return c.AbortNotFound("backup not found")
	}
	return streamLogDownload(c, h.logs, b.LogRef, b.Logs, "backup-"+strconv.FormatUint(uint64(b.ID), 10)+".log")
}

// S3Body is the S3 destination configuration shared by backup requests.
type S3Body struct {
	Endpoint       string `json:"endpoint"`
	Bucket         string `json:"bucket"`
	Region         string `json:"region"`
	AccessKey      string `json:"access_key"`
	SecretKey      string `json:"secret_key"`
	Path           string `json:"path"`
	UseSSL         bool   `json:"use_ssl"`
	ForcePathStyle bool   `json:"force_path_style"`
}

func (b *S3Body) toConfig() *backup.S3Config {
	if b == nil {
		return nil
	}
	return &backup.S3Config{
		Endpoint: b.Endpoint, Bucket: b.Bucket, Region: b.Region,
		AccessKey: b.AccessKey, SecretKey: b.SecretKey, Path: b.Path,
		UseSSL: b.UseSSL, ForcePathStyle: b.ForcePathStyle,
	}
}

type RunBackupRequest struct {
	Body struct {
		Destination string  `json:"destination" enum:"local,s3"`
		S3          *S3Body `json:"s3"`
	} `json:"body"`
}

type RestoreRequest struct {
	Body struct {
		S3 *S3Body `json:"s3"`
		// Method: "normal" (default) restores over the existing database; "force"
		// drops & recreates it first.
		Method string `json:"method" enum:"normal,force"`
	} `json:"body"`
}

type CreateScheduleRequest struct {
	Body struct {
		Cron          string `json:"cron" required:"true"`
		MaxBackups    int    `json:"max_backups" min:"0" max:"1000"`
		RetentionDays int    `json:"retention_days" min:"0" max:"3650"`
	} `json:"body"`
}

func (h *BackupHandler) List(c *okapi.Context) error {
	db, err := h.loadDB(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	backups, err := h.svc.List(db.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list backups", err)
	}
	return ok(c, backups)
}

// Run triggers a manual backup (synchronous).
func (h *BackupHandler) Run(c *okapi.Context, req *RunBackupRequest) error {
	db, err := h.loadDB(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	inst, err := h.instanceFor(db)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	dest := backup.Destination{Type: req.Body.Destination, S3: req.Body.S3.toConfig()}
	// Prefer the workspace's centralized S3 target when configured and the request
	// didn't supply its own S3 config — so manual backups go to S3 whenever the
	// workspace has backup settings set up, instead of defaulting to local.
	if dest.S3 == nil && h.settings != nil {
		if cfg, path, terr := h.settings.DatabaseBackupTarget(db.WorkspaceID); terr == nil && cfg != nil {
			cfg.Path = path
			dest = backup.Destination{Type: "s3", S3: cfg}
		}
	}
	b, err := h.svc.Run(c.Request().Context(), inst, db, "manual", dest)
	if err != nil {
		if errors.Is(err, backup.ErrUnsupportedEngine) {
			return c.AbortBadRequest("backups are not supported for this engine yet")
		}
		return c.AbortInternalServerError("failed to run backup", err)
	}
	h.record(c, db.WorkspaceID, "backup.run", b.ID)
	// Backup runs synchronously; the record's status reflects success/failure.
	return ok(c, b)
}

// Restore restores a database from a backup.
func (h *BackupHandler) Restore(c *okapi.Context, req *RestoreRequest) error {
	db, err := h.loadDB(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	inst, err := h.instanceFor(db)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	b, err := h.loadBackup(c, db.WorkspaceID)
	if err != nil {
		return c.AbortNotFound("backup not found")
	}
	dest := backup.Destination{Type: b.Destination, S3: req.Body.S3.toConfig()}
	// For an S3 backup with no per-request credentials, fall back to the
	// workspace's centralized S3 target (how the backup was created).
	if b.Destination == "s3" && dest.S3 == nil && h.settings != nil {
		if cfg, _, terr := h.settings.DatabaseBackupTarget(db.WorkspaceID); terr == nil && cfg != nil {
			dest.S3 = cfg
		}
	}
	if err := h.svc.RestoreFromBackup(c.Request().Context(), inst, db, b, dest, req.Body.Method == "force"); err != nil {
		return c.AbortInternalServerError("restore failed", err)
	}
	h.record(c, db.WorkspaceID, "backup.restore", b.ID)
	return message(c, "database restored")
}

func allowedDump(name string) bool {
	name = strings.ToLower(name)
	return strings.HasSuffix(name, ".sql.gz") || strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".dump")
}

// RestoreFile restores a logical database from an uploaded dump (multipart). The
// "method" form field selects normal/force.
func (h *BackupHandler) RestoreFile(c *okapi.Context) error {
	db, err := h.loadDB(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	inst, err := h.instanceFor(db)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return c.AbortBadRequest("a dump file is required (field 'file')")
	}
	defer func() { _ = file.Close() }()
	if !allowedDump(header.Filename) {
		return c.AbortBadRequest("unsupported file type (use .sql.gz, .sql, or .dump)")
	}
	if h.maxRestoreBytes > 0 && header.Size > h.maxRestoreBytes {
		return c.AbortBadRequest("file exceeds the maximum restore size")
	}
	force := c.Request().FormValue("method") == "force"
	if err := h.svc.RestoreUpload(c.Request().Context(), inst, db, header.Filename, file, header.Size, force); err != nil {
		return c.AbortInternalServerError("restore failed", err)
	}
	h.record(c, db.WorkspaceID, "backup.restore_upload", db.ID)
	return message(c, "database restored from upload")
}

// Delete removes a backup and (for local backups) its artifact file.
func (h *BackupHandler) Delete(c *okapi.Context) error {
	db, err := h.loadDB(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	b, err := h.loadBackup(c, db.WorkspaceID)
	if err != nil {
		return c.AbortNotFound("backup not found")
	}
	if err := h.svc.Delete(c.Request().Context(), b); err != nil {
		return c.AbortInternalServerError("failed to delete backup", err)
	}
	h.record(c, db.WorkspaceID, "backup.delete", b.ID)
	return message(c, "backup deleted")
}

// Download streams a local backup artifact to the client.
func (h *BackupHandler) Download(c *okapi.Context) error {
	db, err := h.loadDB(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	b, err := h.loadBackup(c, db.WorkspaceID)
	if err != nil {
		return c.AbortNotFound("backup not found")
	}
	rc, size, filename, err := h.svc.Download(c.Request().Context(), b)
	if err != nil {
		switch {
		case errors.Is(err, backup.ErrDownloadRemote):
			return c.AbortBadRequest(err.Error())
		case errors.Is(err, backup.ErrNoBackupFile):
			return c.AbortBadRequest("backup has no downloadable artifact")
		default:
			return c.AbortInternalServerError("failed to read backup", err)
		}
	}
	defer func() { _ = rc.Close() }()

	h.record(c, db.WorkspaceID, "backup.download", b.ID)
	c.SetHeader("Content-Type", "application/gzip")
	c.SetHeader("Content-Disposition", `attachment; filename="`+filename+`"`)
	if size > 0 {
		c.SetHeader("Content-Length", strconv.FormatInt(size, 10))
	}
	w := c.Response()
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
	return nil
}

func (h *BackupHandler) ListSchedules(c *okapi.Context) error {
	db, err := h.loadDB(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	schedules, err := h.repo.ListSchedulesByDatabase(db.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list schedules", err)
	}
	return ok(c, schedules)
}

func (h *BackupHandler) CreateSchedule(c *okapi.Context, req *CreateScheduleRequest) error {
	db, err := h.loadDB(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	// Reject an invalid cron expression before it reaches the scheduler (where a
	// bad spec would silently never fire).
	if err := cronpkg.ValidateSpec(req.Body.Cron); err != nil {
		return c.AbortBadRequest("invalid cron expression")
	}
	// Scheduled backups follow the workspace's backup settings: the centralized
	// S3 target when configured (resolved at run time by the cron manager), else
	// local. The schedule itself carries no S3 config.
	s := &models.BackupSchedule{
		WorkspaceID: db.WorkspaceID, DatabaseID: db.ID,
		Cron: req.Body.Cron, Destination: "workspace", Enabled: true,
		MaxBackups: req.Body.MaxBackups, RetentionDays: req.Body.RetentionDays,
	}
	if err := h.repo.CreateSchedule(s); err != nil {
		return c.AbortInternalServerError("failed to create schedule", err)
	}
	h.cron.Register(*s)
	h.record(c, db.WorkspaceID, "backup.schedule_create", s.ID)
	return created(c, s)
}

func (h *BackupHandler) DeleteSchedule(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	id, err := strconv.Atoi(c.Param("scheduleID"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid schedule id")
	}
	s, err := h.repo.FindScheduleInWorkspace(wsID, uint(id))
	if err != nil {
		return c.AbortNotFound("schedule not found")
	}
	h.cron.Unregister(s.ID)
	if err := h.repo.DeleteSchedule(s.ID); err != nil {
		return c.AbortInternalServerError("failed to delete schedule", err)
	}
	h.record(c, wsID, "backup.schedule_delete", s.ID)
	return message(c, "schedule deleted")
}

func (h *BackupHandler) loadDB(c *okapi.Context) (*models.Database, error) {
	id, err := strconv.Atoi(c.Param("dbID"))
	if err != nil || id <= 0 {
		return nil, errors.New("invalid database id")
	}
	return h.dbs.FindDatabaseInWorkspace(middlewares.WorkspaceID(c), uint(id))
}

func (h *BackupHandler) instanceFor(db *models.Database) (*models.DatabaseInstance, error) {
	return h.dbs.FindByID(db.InstanceID)
}

func (h *BackupHandler) loadBackup(c *okapi.Context, workspaceID uint) (*models.Backup, error) {
	id, err := strconv.Atoi(c.Param("backupID"))
	if err != nil || id <= 0 {
		return nil, errors.New("invalid backup id")
	}
	return h.repo.FindInWorkspace(workspaceID, uint(id))
}

func (h *BackupHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "backup", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
