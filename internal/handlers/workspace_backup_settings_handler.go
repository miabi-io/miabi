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

// S3SecretKey empty keeps the stored secret.
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

		// Only the *Clear flags remove encryption, so it is never lost by an empty field submitted by accident.
		BundlePath            string `json:"bundle_path"`
		BundlePassphrase      string `json:"bundle_passphrase"`
		BundlePassphraseClear bool   `json:"bundle_passphrase_clear"`
		BackupPassphrase      string `json:"backup_passphrase"`
		BackupPassphraseClear bool   `json:"backup_passphrase_clear"`

		// AgeIdentity is write-only and lets the platform restore and verify age backups itself.
		BackupEncryption string `json:"backup_encryption" enum:",gpg,age"`
		AgeRecipients    string `json:"age_recipients"`
		AgeIdentity      string `json:"age_identity"`
		AgeIdentityClear bool   `json:"age_identity_clear"`
		// SkipBackupVerify turns off reading each backup back right after it is written.
		SkipBackupVerify bool `json:"skip_backup_verify"`
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
	passphrase := passphraseChange(b.BundlePassphrase, b.BundlePassphraseClear)
	backupPass := passphraseChange(b.BackupPassphrase, b.BackupPassphraseClear)
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
		BackupEncryption:   b.BackupEncryption,
		AgeRecipients:      b.AgeRecipients,
		AgeIdentity:        passphraseChange(b.AgeIdentity, b.AgeIdentityClear),
		SkipBackupVerify:   b.SkipBackupVerify,
	})
	if err != nil {
		// These are the user's to fix, not internal failures.
		if errors.Is(err, wsbundle.ErrWeakPassphrase) || errors.Is(err, backupsettings.ErrSealedSetsExist) || errors.Is(err, backupsettings.ErrInvalidAge) {
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

// nil keeps the stored value, "" removes encryption, anything else replaces it.
func passphraseChange(value string, clear bool) *string {
	switch {
	case clear:
		empty := ""
		return &empty
	case value != "":
		return &value
	}
	return nil
}

func (h *WorkspaceBackupSettingsHandler) Test(c *okapi.Context, req *UpdateBackupSettingsRequest) error {
	wsID := middlewares.WorkspaceID(c)
	b := req.Body
	var secret *string
	if b.S3SecretKey != "" {
		secret = &b.S3SecretKey
	}
	// The blob client's timeout is sized for uploads, not for a person waiting on a button.
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

type TestBackupSettingsResponse struct {
	OK      bool                         `json:"ok"`
	Message string                       `json:"message"`
	Checks  []backupsettings.PrefixCheck `json:"checks"`
}

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
