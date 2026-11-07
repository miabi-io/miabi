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

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/registryserver"
)

// S3/MinIO registry storage is gated behind FlagRegistryS3; local storage is Community.
type AdminRegistryHandler struct {
	svc       *registryserver.Service
	ee        enterprise.EE
	audit     *audit.Logger
	ensure    func(context.Context) error
	gc        func(context.Context) error
	runtime   func(context.Context) (*registryserver.Runtime, error)
	repoCount func(context.Context) (int, error)
}

// SetRuntime wires the live container status/usage reader.
func (h *AdminRegistryHandler) SetRuntime(fn func(context.Context) (*registryserver.Runtime, error)) {
	h.runtime = fn
}

func (h *AdminRegistryHandler) SetRepoCount(fn func(context.Context) (int, error)) {
	h.repoCount = fn
}

func NewAdminRegistryHandler(svc *registryserver.Service, ee enterprise.EE, auditLog *audit.Logger) *AdminRegistryHandler {
	return &AdminRegistryHandler{svc: svc, ee: ee, audit: auditLog}
}

func (h *AdminRegistryHandler) SetEnsure(fn func(context.Context) error) { h.ensure = fn }

// SetGC wires the on-demand garbage-collection callback.
func (h *AdminRegistryHandler) SetGC(fn func(context.Context) error) { h.gc = fn }

// RunGC triggers a registry garbage collection (read-only during the collect).
func (h *AdminRegistryHandler) RunGC(c *okapi.Context) error {
	if h.gc == nil {
		return c.AbortInternalServerError("garbage collection is unavailable", nil)
	}
	if err := h.gc(c.Request().Context()); err != nil {
		return c.AbortInternalServerError("garbage collection failed", err)
	}
	h.record(c, "registry.gc")
	return message(c, "garbage collection complete")
}

// Pointer fields: nil means leave alone, so a partial save from one tab cannot clobber another's.
type UpdateRegistrySettingsRequest struct {
	Body RegistrySettingsBody `json:"body"`
}

type RegistrySettingsBody struct {
	DeleteEnabled       bool `json:"delete_enabled"`
	PerWorkspaceQuotaMB int  `json:"per_workspace_quota_mb"`

	Enabled     *bool   `json:"enabled"`
	Host        *string `json:"host"`
	StorageType *string `json:"storage_type" enum:"filesystem,s3"`
	// StorageClass empty or "default" is Docker's data root.
	StorageClass *string `json:"storage_class"`

	S3Endpoint       *string `json:"s3_endpoint"`
	S3Bucket         *string `json:"s3_bucket"`
	S3Region         *string `json:"s3_region"`
	S3AccessKey      *string `json:"s3_access_key"`
	S3SecretKey      *string `json:"s3_secret_key"` // blank keeps the stored one
	S3ForcePathStyle *bool   `json:"s3_force_path_style"`

	// Confirm is required for changes the platform cannot undo: moving the hostname or switching storage backend.
	Confirm bool `json:"confirm"`
}

type RegistrySettingsView struct {
	*models.RegistrySettings
	EffectiveHost string               `json:"effective_host"`
	S3Entitled    bool                 `json:"s3_entitled"`
	Locks         registryserver.Locks `json:"locks"`
	HostSource    string               `json:"host_source"`
	StorageSource string               `json:"storage_source"`
	StorageError  string               `json:"storage_error,omitempty"`
}

func (h *AdminRegistryHandler) view(st *models.RegistrySettings) RegistrySettingsView {
	return RegistrySettingsView{
		RegistrySettings: st,
		EffectiveHost:    h.svc.HostFor(st),
		S3Entitled:       h.ee.Has(enterprise.FlagRegistryS3),
		Locks:            h.svc.Locks(),
		HostSource:       h.svc.HostSource(st),
		StorageSource:    h.svc.StorageSource(st),
		StorageError:     h.svc.StorageUnavailableReason(st),
	}
}

// Runtime returns the registry container's live state and resource usage.
func (h *AdminRegistryHandler) Runtime(c *okapi.Context) error {
	if h.runtime == nil {
		return c.AbortInternalServerError("registry runtime status is unavailable", nil)
	}
	rt, err := h.runtime(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to read registry status", err)
	}
	return ok(c, rt)
}

// GetSettings returns the registry settings (secret omitted).
func (h *AdminRegistryHandler) GetSettings(c *okapi.Context) error {
	st, err := h.svc.Get()
	if err != nil {
		return c.AbortInternalServerError("failed to load registry settings", err)
	}
	return ok(c, h.view(st))
}

// Hostname and storage changes need confirmation: references are anchored to the host and blobs do not migrate.
func (h *AdminRegistryHandler) UpdateSettings(c *okapi.Context, req *UpdateRegistrySettingsRequest) error {
	b := req.Body
	if b.PerWorkspaceQuotaMB < 0 {
		return c.AbortBadRequest("the per-workspace quota cannot be negative (0 = unlimited)")
	}
	current, err := h.svc.Get()
	if err != nil {
		return c.AbortInternalServerError("failed to load registry settings", err)
	}
	locks := h.svc.Locks()

	// Checked again at container start, covering the env path and a licence that lapses later.
	wantsS3 := current.UsesS3()
	if b.StorageType != nil && !locks.Storage {
		wantsS3 = *b.StorageType == models.RegistryStorageS3
	}
	if wantsS3 && !h.ee.Has(enterprise.FlagRegistryS3) {
		return c.AbortWithError(402, errors.New(
			"S3/MinIO storage for the built-in registry requires an Enterprise license (the registry_s3 entitlement)"))
	}

	if b.StorageClass != nil && !locks.StorageClass &&
		models.NormalizeRegistryStorageClass(*b.StorageClass) != current.StorageClass {
		if err := h.svc.ValidateStorageClass(*b.StorageClass); err != nil {
			return c.AbortBadRequest("storage class: " + err.Error())
		}
	}

	if msg := h.destructiveChange(c.Request().Context(), current, locks, b); msg != "" && !b.Confirm {
		// The message is the confirmation prompt the UI shows verbatim.
		return c.AbortWithError(409, errors.New(msg))
	}

	st, err := h.svc.Save(registryserver.SaveInput{
		DeleteEnabled:       b.DeleteEnabled,
		PerWorkspaceQuotaMB: b.PerWorkspaceQuotaMB,
		Enabled:             b.Enabled,
		Host:                b.Host,
		StorageType:         b.StorageType,
		StorageClass:        b.StorageClass,
		S3Endpoint:          b.S3Endpoint,
		S3Bucket:            b.S3Bucket,
		S3Region:            b.S3Region,
		S3AccessKey:         b.S3AccessKey,
		S3SecretKey:         b.S3SecretKey,
		S3ForcePathStyle:    b.S3ForcePathStyle,
	})
	if err != nil {
		// A rejected hostname is the admin's typo, not a server fault.
		if strings.HasPrefix(err.Error(), "host:") {
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to save registry settings", err)
	}
	// Best-effort so a Docker hiccup doesn't fail the settings save.
	if h.ensure != nil {
		if err := h.ensure(c.Request().Context()); err != nil {
			logger.Warn("registry ensure after settings change failed", "error", err)
		}
	}
	h.record(c, "registry.settings_update")
	return ok(c, h.view(st))
}

// Only destructive against something that exists: moving an unused registry's host costs nothing.
func (h *AdminRegistryHandler) destructiveChange(
	ctx context.Context, current *models.RegistrySettings, locks registryserver.Locks, b RegistrySettingsBody,
) string {
	repos := h.storedRepoCount(ctx)

	if b.Host != nil && !locks.Host {
		want, err := registryserver.NormalizeHost(*b.Host)
		if err == nil && want != "" && current.Host != "" && want != current.Host {
			return "Changing the registry hostname from " + current.Host + " to " + want +
				" leaves every image reference Miabi has already recorded pointing at the old name." +
				" Existing deployments keep referencing " + current.Host + " and will fail to pull until they are redeployed."
		}
	}

	// Nothing stored yet — no blobs to strand, so a storage change is free.
	if repos <= 0 {
		return ""
	}
	if b.StorageType != nil && !locks.Storage {
		want := *b.StorageType
		if (want == models.RegistryStorageS3) != current.UsesS3() {
			return "Switching the storage driver abandons the " + plural(repos, "repository", "repositories") +
				" already pushed: blobs are not migrated between backends, and the images stay in the old one."
		}
	}
	if !current.UsesS3() && b.StorageClass != nil && !locks.StorageClass {
		want := &models.RegistrySettings{StorageClass: models.NormalizeRegistryStorageClass(*b.StorageClass)}
		if want.DataVolume() != current.DataVolume() {
			return "Moving the registry to the " + storageClassLabel(want.StorageClass) + " storage class starts it on an empty volume (" +
				want.DataVolume() + "). The " + plural(repos, "repository", "repositories") + " already pushed stay in " +
				current.DataVolume() + " and are not copied."
		}
	}
	if current.UsesS3() && b.S3Bucket != nil && !locks.S3Bucket {
		if want := strings.TrimSpace(*b.S3Bucket); want != "" && current.S3Bucket != "" && want != current.S3Bucket {
			return "Pointing the registry at bucket " + want + " abandons the " +
				plural(repos, "repository", "repositories") + " stored in " + current.S3Bucket + "."
		}
	}
	return ""
}

// An unreachable registry counts as empty so a probe failure can't wedge the form behind a confirmation.
func (h *AdminRegistryHandler) storedRepoCount(ctx context.Context) int {
	if h.repoCount == nil {
		return 0
	}
	n, err := h.repoCount(ctx)
	if err != nil {
		logger.Warn("registry: repository count unavailable for the storage-change check", "error", err)
		return 0
	}
	return n
}

func storageClassLabel(name string) string {
	if name == "" {
		return models.DefaultStorageClassName
	}
	return name
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func (h *AdminRegistryHandler) record(c *okapi.Context, action string) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: action, TargetType: "registry", IP: c.RealIP()})
}
