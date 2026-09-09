// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dbenvelope"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// WorkspaceBackupSettingsHandler exposes a workspace's shared S3 backup target.
type WorkspaceBackupSettingsHandler struct {
	svc   *backupsettings.Service
	audit *audit.Logger
}

func NewWorkspaceBackupSettingsHandler(svc *backupsettings.Service, auditLog *audit.Logger) *WorkspaceBackupSettingsHandler {
	return &WorkspaceBackupSettingsHandler{svc: svc, audit: auditLog}
}

// UpdateBackupSettingsRequest is the body for updating (and validating) settings.
// S3SecretKey is empty to keep the stored secret unchanged.
type UpdateBackupSettingsRequest struct {
	Body struct {
		S3Enabled        bool   `json:"s3_enabled"`
		S3Endpoint       string `json:"s3_endpoint"`
		S3Bucket         string `json:"s3_bucket"`
		S3Region         string `json:"s3_region"`
		S3AccessKey      string `json:"s3_access_key"`
		S3SecretKey      string `json:"s3_secret_key"`
		S3UseSSL         bool   `json:"s3_use_ssl"`
		S3ForcePathStyle bool   `json:"s3_force_path_style"`

		DatabaseBackupPath string `json:"database_backup_path"`
		VolumeBackupPath   string `json:"volume_backup_path"`

		BundlePath       string `json:"bundle_path"`
		BundlePassphrase string `json:"bundle_passphrase"`

		// BackupPassphrase encrypts database backups. Omitted keeps the stored value;
		// BackupPassphraseClear is the only way to go back to cleartext, so that
		// clearing it is always deliberate and never an empty field submitted by accident.
		BackupPassphrase      string `json:"backup_passphrase"`
		BackupPassphraseClear bool   `json:"backup_passphrase_clear"`
	} `json:"body"`
}

// Get returns the workspace's backup settings (secret omitted).
func (h *WorkspaceBackupSettingsHandler) Get(c *okapi.Context) error {
	st, err := h.svc.Get(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to load backup settings", err)
	}
	return ok(c, st)
}

// Update upserts the workspace's backup settings.
func (h *WorkspaceBackupSettingsHandler) Update(c *okapi.Context, req *UpdateBackupSettingsRequest) error {
	wsID := middlewares.WorkspaceID(c)
	b := req.Body
	if b.S3Enabled && b.S3Bucket == "" {
		return c.AbortBadRequest("an S3 bucket is required when S3 is enabled")
	}
	var secret *string
	if b.S3SecretKey != "" {
		secret = &b.S3SecretKey
	}
	var passphrase *string
	if b.BundlePassphrase != "" {
		passphrase = &b.BundlePassphrase
	}
	var backupPass *string
	switch {
	case b.BackupPassphraseClear:
		empty := ""
		backupPass = &empty
	case b.BackupPassphrase != "":
		backupPass = &b.BackupPassphrase
	}
	st, err := h.svc.Save(wsID, backupsettings.SaveInput{
		S3Enabled:          b.S3Enabled,
		S3Endpoint:         b.S3Endpoint,
		S3Bucket:           b.S3Bucket,
		S3Region:           b.S3Region,
		S3AccessKey:        b.S3AccessKey,
		S3SecretKey:        secret,
		S3UseSSL:           b.S3UseSSL,
		S3ForcePathStyle:   b.S3ForcePathStyle,
		DatabaseBackupPath: b.DatabaseBackupPath,
		VolumeBackupPath:   b.VolumeBackupPath,
		BundlePath:         b.BundlePath,
		BundlePassphrase:   passphrase,
		BackupPassphrase:   backupPass,
	})
	if err != nil {
		// A rejected passphrase is the user's to fix, not an internal failure. So is
		// discarding one that recovery points are still sealed with.
		if errors.Is(err, wsbundle.ErrWeakPassphrase) || errors.Is(err, backupsettings.ErrSealedSetsExist) {
			return c.AbortBadRequest(err.Error())
		}
		if errors.Is(err, dbenvelope.ErrBadPassphrase) {
			return c.AbortBadRequest("the stored passphrase no longer opens this workspace's recovery points, so it cannot be rotated")
		}
		return c.AbortInternalServerError("failed to save backup settings", err)
	}
	h.record(c, wsID, "backup.settings_update")
	return ok(c, st)
}

// Test proves the supplied (or stored) S3 target works, by using it: under every
// prefix this workspace writes to, it puts a small object, reads it back and
// removes it.
func (h *WorkspaceBackupSettingsHandler) Test(c *okapi.Context, req *UpdateBackupSettingsRequest) error {
	wsID := middlewares.WorkspaceID(c)
	b := req.Body
	var secret *string
	if b.S3SecretKey != "" {
		secret = &b.S3SecretKey
	}
	// Bounded: a wrong endpoint should answer in seconds. The blob client's own
	// timeout is sized for uploading artifacts, not for a person waiting on a
	// button.
	ctx, cancel := context.WithTimeout(c.Request().Context(), 25*time.Second)
	defer cancel()

	checks, err := h.svc.TestTarget(ctx, wsID, backupsettings.SaveInput{
		S3Endpoint: b.S3Endpoint, S3Bucket: b.S3Bucket, S3Region: b.S3Region,
		S3AccessKey: b.S3AccessKey, S3SecretKey: secret,
		S3UseSSL: b.S3UseSSL, S3ForcePathStyle: b.S3ForcePathStyle,
		DatabaseBackupPath: b.DatabaseBackupPath, VolumeBackupPath: b.VolumeBackupPath,
		BundlePath: b.BundlePath,
	})
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	return ok(c, backupTestResult(checks))
}

// TestBackupSettingsResponse is what a connection test reports: whether the
// target is usable, a sentence for the operator, and the per-prefix detail
// behind it.
type TestBackupSettingsResponse struct {
	OK      bool                         `json:"ok"`
	Message string                       `json:"message"`
	Checks  []backupsettings.PrefixCheck `json:"checks"`
}

// backupTestResult turns the probe results into the answer the operator needs:
// what was actually done, or which prefix refused and why.
func backupTestResult(checks []backupsettings.PrefixCheck) TestBackupSettingsResponse {
	res := TestBackupSettingsResponse{OK: true, Checks: checks}
	var failed, undeletable []string
	for _, c := range checks {
		switch {
		case !c.OK():
			res.OK = false
			failed = append(failed, prefixLabel(c.Prefix)+": "+c.Error)
		case !c.Removed:
			undeletable = append(undeletable, prefixLabel(c.Prefix))
		}
	}
	switch {
	case !res.OK:
		res.Message = strings.Join(failed, "; ")
	case len(undeletable) > 0:

		res.Message = "Wrote and read back a test object, but could not delete it in " +
			strings.Join(undeletable, ", ") + ". Backups will work; deleting them will not."
	default:
		res.Message = "Wrote, read back and removed a test object in " +
			strings.Join(prefixLabels(checks), ", ") + "."
	}
	return res
}

func prefixLabel(prefix string) string {
	if prefix == "" {
		return "the bucket root"
	}
	return prefix + "/"
}

func prefixLabels(checks []backupsettings.PrefixCheck) []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, prefixLabel(c.Prefix))
	}
	return out
}

func (h *WorkspaceBackupSettingsHandler) record(c *okapi.Context, wsID uint, action string) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID:     &actor,
		WorkspaceID: &wsID,
		Action:      action,
		TargetType:  "backup_settings",
		TargetID:    strconv.Itoa(int(wsID)),
		IP:          c.RealIP(),
	})
}
