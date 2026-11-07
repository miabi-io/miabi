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

package handlers

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/platformbackup"
)

type AdminPlatformBackupHandler struct {
	svc        *platformbackup.Service
	ee         enterprise.EE
	audit      *audit.Logger
	reschedule func(*models.PlatformBackupSettings)
	logs       *logstore.Store
}

func NewAdminPlatformBackupHandler(svc *platformbackup.Service, ee enterprise.EE, auditLog *audit.Logger) *AdminPlatformBackupHandler {
	return &AdminPlatformBackupHandler{svc: svc, ee: ee, audit: auditLog}
}

// SetLogStore nil keeps tail-only log downloads.
func (h *AdminPlatformBackupHandler) SetLogStore(s *logstore.Store) { h.logs = s }

// LogsDownload streams a platform-backup run's full log as a file download.
func (h *AdminPlatformBackupHandler) LogsDownload(c *okapi.Context) error {
	b, err := h.loadBackup(c)
	if err != nil {
		return c.AbortNotFound("platform backup not found")
	}
	return streamLogDownload(c, h.logs, b.LogRef, b.Logs, "platform-backup-"+strconv.FormatUint(uint64(b.ID), 10)+".log")
}

func (h *AdminPlatformBackupHandler) SetReschedule(fn func(*models.PlatformBackupSettings)) {
	h.reschedule = fn
}

// S3SecretKey empty keeps the stored secret unchanged.
type UpdatePlatformBackupSettingsRequest struct {
	Body struct {
		S3Enabled        bool   `json:"s3_enabled"`
		S3Endpoint       string `json:"s3_endpoint"`
		S3Bucket         string `json:"s3_bucket"`
		S3Region         string `json:"s3_region"`
		S3AccessKey      string `json:"s3_access_key"`
		S3SecretKey      string `json:"s3_secret_key"`
		S3UseSSL         bool   `json:"s3_use_ssl"`
		S3ForcePathStyle bool   `json:"s3_force_path_style"`

		// DatabaseBackupPath and VolumeBackupPath default to <root>/databases and <root>/volumes.
		RootPath           string `json:"root_path"`
		DatabaseBackupPath string `json:"database_backup_path"`
		VolumeBackupPath   string `json:"volume_backup_path"`

		// BackupPassphrase must never be MIABI_ENCRYPTION_KEY; empty keeps the stored one, never returned.
		EncryptBackups   bool   `json:"encrypt_backups"`
		BackupPassphrase string `json:"backup_passphrase"`
		IncludeIdentity  bool   `json:"include_identity"`
		// IncludeTenantData turns a control-plane recovery point into a whole-platform one.
		IncludeTenantData bool `json:"include_tenant_data"`

		ScheduleEnabled bool     `json:"schedule_enabled"`
		ScheduleCron    string   `json:"schedule_cron"`
		MaxBackups      int      `json:"max_backups"`
		RetentionDays   int      `json:"retention_days"`
		Volumes         []string `json:"volumes"`
	} `json:"body"`
}

type CreatePlatformBackupSetRequest struct {
	Body struct{} `json:"body"`
}

type RestoreSelectedRequest struct {
	Body struct {
		// By id, so the request means exactly what the operator was shown.
		ArtifactIDs []uint `json:"artifact_ids"`
		// Empty uses the stored passphrase, correct only for this platform's own recovery points.
		Passphrase string `json:"passphrase"`
		StopApps   bool   `json:"stop_apps"`
		// Confirm is required: this overwrites live data.
		Confirm bool `json:"confirm"`
	} `json:"body"`
}

// ImportRecoveryPointRequest adopts a recovery point found in the bucket.
type ImportRecoveryPointRequest struct {
	Body struct {
		Ref string `json:"ref"`
	} `json:"body"`
}

// The stored passphrase proves the artifacts exist but not that the operator can still open them, so the UI asks.
type VerifyPlatformBackupSetRequest struct {
	Body struct {
		Passphrase string `json:"passphrase"`
	} `json:"body"`
}

// CreatePlatformBackupRequest selects what to back up now.
type CreatePlatformBackupRequest struct {
	Body struct {
		Database bool     `json:"database"` // back up the control-plane DB (default when nothing selected)
		Volumes  []string `json:"volumes"`  // platform volumes to back up
	} `json:"body"`
}

type RestorePlatformBackupRequest struct {
	Body struct {
		Confirm bool `json:"confirm"`
	} `json:"body"`
}

// GetSettings returns the platform backup settings (secret omitted).
func (h *AdminPlatformBackupHandler) GetSettings(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	st, err := h.svc.GetSettings()
	if err != nil {
		return c.AbortInternalServerError("failed to load platform backup settings", err)
	}
	return ok(c, st)
}

// UpdateSettings upserts the platform backup settings.
func (h *AdminPlatformBackupHandler) UpdateSettings(c *okapi.Context, req *UpdatePlatformBackupSettingsRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	b := req.Body
	if b.IncludeTenantData && !h.svc.TenantCaptureAvailable() {
		return c.AbortBadRequest("tenant data capture is unavailable on this deployment")
	}
	// Env-locked deployments don't send bucket/schedule, so SaveSettings validates the effective settings instead.
	var secret *string
	if b.S3SecretKey != "" {
		secret = &b.S3SecretKey
	}
	var passphrase *string
	if b.BackupPassphrase != "" {
		passphrase = &b.BackupPassphrase
	}
	st, err := h.svc.SaveSettings(platformbackup.SaveInput{
		S3Enabled:          b.S3Enabled,
		S3Endpoint:         b.S3Endpoint,
		S3Bucket:           b.S3Bucket,
		S3Region:           b.S3Region,
		S3AccessKey:        b.S3AccessKey,
		S3SecretKey:        secret,
		S3UseSSL:           b.S3UseSSL,
		S3ForcePathStyle:   b.S3ForcePathStyle,
		RootPath:           b.RootPath,
		DatabaseBackupPath: b.DatabaseBackupPath,
		VolumeBackupPath:   b.VolumeBackupPath,
		EncryptBackups:     b.EncryptBackups,
		BackupPassphrase:   passphrase,
		IncludeIdentity:    b.IncludeIdentity,
		IncludeTenantData:  b.IncludeTenantData,
		ScheduleEnabled:    b.ScheduleEnabled,
		ScheduleCron:       b.ScheduleCron,
		MaxBackups:         b.MaxBackups,
		RetentionDays:      b.RetentionDays,
		Volumes:            b.Volumes,
	})
	if err != nil {
		switch {
		case errors.Is(err, dr.ErrWeakPassphrase):
			return c.AbortBadRequest(err.Error())
		case errors.Is(err, platformbackup.ErrNoPassphrase), errors.Is(err, platformbackup.ErrSetNeedsS3):
			return c.AbortBadRequest(err.Error())
		default:
			return c.AbortInternalServerError("failed to save platform backup settings", err)
		}
	}
	if h.reschedule != nil {
		h.reschedule(st)
	}
	h.record(c, "platform.backup.settings_update", 0)
	return ok(c, st)
}

// A live bucket probe runs on the first backup via the one-shot backup tool.
func (h *AdminPlatformBackupHandler) TestSettings(c *okapi.Context, req *UpdatePlatformBackupSettingsRequest) error {
	if err := h.ee.Require(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	b := req.Body
	if b.S3Bucket == "" {
		return c.AbortBadRequest("an S3 bucket is required")
	}
	if b.S3AccessKey == "" {
		return c.AbortBadRequest("an access key is required")
	}
	secretSet := b.S3SecretKey != ""
	if !secretSet {
		if cur, err := h.svc.GetSettings(); err == nil {
			secretSet = cur.S3SecretSet
		}
	}
	if !secretSet {
		return c.AbortBadRequest("a secret key is required")
	}
	return message(c, "platform backup settings look valid")
}

// ListBackups returns the platform backup history (paginated, newest first).
func (h *AdminPlatformBackupHandler) ListBackups(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	items, total, err := h.svc.ListPaged(size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list platform backups", err)
	}
	return paginated(c, items, total, page, size)
}

func (h *AdminPlatformBackupHandler) CreateBackup(c *okapi.Context, req *CreatePlatformBackupRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	ctx := c.Request().Context()
	b := req.Body
	// Default to a database backup when nothing is selected.
	wantDB := b.Database || len(b.Volumes) == 0

	recs := make([]*models.PlatformBackup, 0, len(b.Volumes)+1)
	if wantDB {
		rec, err := h.svc.Create(ctx, models.PlatformBackupDatabase, "", "manual")
		if err != nil {
			return h.createErr(c, err)
		}
		recs = append(recs, rec)
	}
	for _, v := range b.Volumes {
		rec, err := h.svc.Create(ctx, models.PlatformBackupVolume, v, "manual")
		if err != nil {
			return h.createErr(c, err)
		}
		recs = append(recs, rec)
	}
	for _, rec := range recs {
		h.record(c, "platform.backup.create", rec.ID)
	}
	return created(c, recs)
}

func (h *AdminPlatformBackupHandler) createErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, platformbackup.ErrS3NotConfigured):
		return c.AbortBadRequest("configure an S3 target before backing up volumes (volume backups have no local destination)")
	case errors.Is(err, platformbackup.ErrSetNeedsS3), errors.Is(err, platformbackup.ErrNoPassphrase), errors.Is(err, platformbackup.ErrVolumeExcluded):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError("failed to start platform backup", err)
	}
}

func (h *AdminPlatformBackupHandler) ListSets(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	items, total, err := h.svc.ListSetsPaged(size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list recovery points", err)
	}
	return paginated(c, items, total, page, size)
}

// GetSet returns one recovery point with its artifacts.
func (h *AdminPlatformBackupHandler) GetSet(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	set, err := h.loadSet(c)
	if err != nil {
		return c.AbortNotFound("recovery point not found")
	}
	return ok(c, set)
}

// CreateSet takes a full recovery point now.
func (h *AdminPlatformBackupHandler) CreateSet(c *okapi.Context, _ *CreatePlatformBackupSetRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	set, err := h.svc.CreateSet(c.Request().Context(), "manual")
	if err != nil {
		return h.createErr(c, err)
	}
	h.record(c, "platform.backup.set_create", set.ID)
	return created(c, set)
}

// VerifySet is the drill that turns a backup into a recovery point: a set never opened is a hypothesis.
func (h *AdminPlatformBackupHandler) VerifySet(c *okapi.Context, req *VerifyPlatformBackupSetRequest) error {
	if err := h.ee.Require(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	set, err := h.loadSet(c)
	if err != nil {
		return c.AbortNotFound("recovery point not found")
	}
	report, err := h.svc.VerifySet(c.Request().Context(), set, req.Body.Passphrase)
	if err != nil {
		return c.AbortInternalServerError("failed to verify recovery point", err)
	}
	h.record(c, "platform.backup.set_verify", set.ID)
	return ok(c, report)
}

// The bucket is the authority: after a restore the database only knows points its dump contained.
func (h *AdminPlatformBackupHandler) Discover(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	sets, err := h.svc.DiscoverSets(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to read the backup target", err)
	}
	return ok(c, sets)
}

func (h *AdminPlatformBackupHandler) Import(c *okapi.Context, req *ImportRecoveryPointRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	if req.Body.Ref == "" {
		return c.AbortBadRequest("a recovery point ref is required")
	}
	set, err := h.svc.ImportSet(c.Request().Context(), req.Body.Ref)
	if err != nil {
		return c.AbortInternalServerError("failed to import the recovery point", err)
	}
	h.record(c, "platform.backup.set_import", set.ID)
	return ok(c, set)
}

func (h *AdminPlatformBackupHandler) RestoreSelected(c *okapi.Context, req *RestoreSelectedRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	if !req.Body.Confirm {
		return c.AbortBadRequest("restoring overwrites live data; it must be explicitly confirmed")
	}
	set, err := h.loadSet(c)
	if err != nil {
		return c.AbortNotFound("recovery point not found")
	}
	report, err := h.svc.RestoreSelected(c.Request().Context(), set, platformbackup.RestoreSelection{
		ArtifactIDs: req.Body.ArtifactIDs,
		Passphrase:  req.Body.Passphrase,
		StopApps:    req.Body.StopApps,
	})
	if err != nil {
		switch {
		case errors.Is(err, platformbackup.ErrNothingSelected),
			errors.Is(err, platformbackup.ErrNotRestorableHere),
			errors.Is(err, platformbackup.ErrS3NotConfigured):
			return c.AbortBadRequest(err.Error())
		default:
			return c.AbortInternalServerError("restore failed", err)
		}
	}
	h.record(c, "platform.backup.restore_selected", set.ID)
	return ok(c, report)
}

func (h *AdminPlatformBackupHandler) RetrySet(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	set, err := h.loadSet(c)
	if err != nil {
		return c.AbortNotFound("recovery point not found")
	}
	if set.Status == models.BackupPending || set.Status == models.BackupRunning {
		return c.AbortBadRequest("this recovery point is still running")
	}
	out, err := h.svc.RetrySet(c.Request().Context(), set)
	if err != nil {
		if errors.Is(err, platformbackup.ErrNothingToRetry) {
			return c.AbortBadRequest(err.Error())
		}
		return h.createErr(c, err)
	}
	h.record(c, "platform.backup.set_retry", set.ID)
	return ok(c, out)
}

// DeleteSet removes a recovery point and its artifacts.
func (h *AdminPlatformBackupHandler) DeleteSet(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	set, err := h.loadSet(c)
	if err != nil {
		return c.AbortNotFound("recovery point not found")
	}
	if set.Status == models.BackupPending || set.Status == models.BackupRunning {
		return c.AbortBadRequest("cannot delete a recovery point that is still running")
	}
	if err := h.svc.DeleteSet(c.Request().Context(), set); err != nil {
		return c.AbortInternalServerError("failed to delete recovery point", err)
	}
	h.record(c, "platform.backup.set_delete", set.ID)
	return message(c, "recovery point deleted")
}

func (h *AdminPlatformBackupHandler) loadSet(c *okapi.Context) (*models.PlatformBackupSet, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		return nil, err
	}
	return h.svc.GetSet(uint(id))
}

// The original MIABI_ENCRYPTION_KEY is still required afterwards to decrypt the restored ciphertext.
func (h *AdminPlatformBackupHandler) Restore(c *okapi.Context, req *RestorePlatformBackupRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	if !req.Body.Confirm {
		return c.AbortBadRequest("restore must be explicitly confirmed")
	}
	b, err := h.loadBackup(c)
	if err != nil {
		return c.AbortNotFound("platform backup not found")
	}
	if b.Status != models.BackupCompleted {
		return c.AbortBadRequest("only a completed backup can be restored")
	}
	if err := h.svc.Restore(c.Request().Context(), b); err != nil {
		switch {
		case errors.Is(err, platformbackup.ErrNoArtifact):
			return c.AbortBadRequest("this backup has no artifact to restore")
		case errors.Is(err, platformbackup.ErrS3NotConfigured):
			return c.AbortBadRequest("configure the S3 target before restoring")
		default:
			return c.AbortInternalServerError("restore failed", err)
		}
	}
	h.record(c, "platform.backup.restore", b.ID)
	return message(c, "platform backup restored")
}

// Delete removes a platform backup record (and the local artifact, if any).
func (h *AdminPlatformBackupHandler) Delete(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	b, err := h.loadBackup(c)
	if err != nil {
		return c.AbortNotFound("platform backup not found")
	}
	if b.Status == models.BackupPending || b.Status == models.BackupRunning {
		return c.AbortBadRequest("cannot delete a backup that is still running")
	}
	if err := h.svc.Delete(c.Request().Context(), b); err != nil {
		return c.AbortInternalServerError("failed to delete platform backup", err)
	}
	h.record(c, "platform.backup.delete", b.ID)
	return message(c, "platform backup deleted")
}

// Download streams a local DB backup artifact to the client.
func (h *AdminPlatformBackupHandler) Download(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	b, err := h.loadBackup(c)
	if err != nil {
		return c.AbortNotFound("platform backup not found")
	}
	rc, size, filename, err := h.svc.Download(c.Request().Context(), b)
	if err != nil {
		switch {
		case errors.Is(err, platformbackup.ErrDownloadRemote):
			return c.AbortBadRequest(err.Error())
		case errors.Is(err, platformbackup.ErrNoArtifact):
			return c.AbortBadRequest("backup has no downloadable artifact")
		default:
			return c.AbortInternalServerError("failed to read backup", err)
		}
	}
	defer func() { _ = rc.Close() }()

	h.record(c, "platform.backup.download", b.ID)
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

// Volumes discovers candidate platform/system volumes the admin can include.
func (h *AdminPlatformBackupHandler) Volumes(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagPlatformBackup); err != nil {
		return entitlementAbort(c, err)
	}
	vols, err := h.svc.DiscoverVolumes(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to discover platform volumes", err)
	}
	return ok(c, vols)
}

func (h *AdminPlatformBackupHandler) loadBackup(c *okapi.Context) (*models.PlatformBackup, error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return nil, errors.New("invalid backup id")
	}
	return h.svc.Get(uint(id))
}

func (h *AdminPlatformBackupHandler) record(c *okapi.Context, action string, id uint) {
	actor := middlewares.UserID(c)
	target := ""
	if id > 0 {
		target = strconv.Itoa(int(id))
	}
	h.audit.Record(audit.Entry{
		ActorID:    &actor,
		Action:     action,
		TargetType: "platform_backup",
		TargetID:   target,
		IP:         c.RealIP(),
	})
}
