// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/registryserver"
)

// AdminRegistryHandler exposes the platform's built-in Docker registry settings
// to the super-admin. The registry itself is a Community feature (local storage);
// S3/MinIO storage is gated behind the FlagRegistryS3 entitlement.
type AdminRegistryHandler struct {
	svc   *registryserver.Service
	ee    enterprise.EE
	audit *audit.Logger
	// ensure re-creates/tears down the registry container after a settings change.
	// Injected by the routes layer (it owns the control-plane Docker client).
	ensure func(context.Context) error
	// gc runs a garbage collection on demand.
	gc func(context.Context) error
}

func NewAdminRegistryHandler(svc *registryserver.Service, ee enterprise.EE, auditLog *audit.Logger) *AdminRegistryHandler {
	return &AdminRegistryHandler{svc: svc, ee: ee, audit: auditLog}
}

// SetEnsure wires the callback that applies the settings to the running
// container (recreate on change, tear down when disabled).
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

// UpdateRegistrySettingsRequest is the body for updating the registry settings:
// the two operational knobs an admin may turn at runtime.
//
// Enablement, the hostname, and the storage configuration are not in this body.
// They are fixed at boot from MIABI_REGISTRY_ENABLED / MIABI_REGISTRY_HOST /
// MIABI_REGISTRY_STORAGE + MIABI_REGISTRY_S3_* and take a restart to change:
// they determine whether the registry exists, what name every image reference on
// the platform is anchored to, and where its blobs live. None of that may be
// movable by an API call while deployments hold references to the old value and
// blobs sit in the old backend.
type UpdateRegistrySettingsRequest struct {
	Body struct {
		DeleteEnabled       bool `json:"delete_enabled"`
		PerWorkspaceQuotaMB int  `json:"per_workspace_quota_mb"`
	} `json:"body"`
}

// RegistrySettingsView is the settings response enriched with the effective host
// and the read-only storage explanation the UI renders.
type RegistrySettingsView struct {
	*models.RegistrySettings
	EffectiveHost string `json:"effective_host"`
	S3Entitled    bool   `json:"s3_entitled"`
	// HostLocked reports that enablement and the hostname are read-only here:
	// they come from MIABI_REGISTRY_ENABLED / MIABI_REGISTRY_HOST and take a
	// restart to change. Always true — it exists so the UI can render the two
	// fields as fixed and explain why, rather than offering inputs whose values
	// the server discards.
	HostLocked bool `json:"host_locked"`
	// HostSource is where the effective host came from: "env", "stored" (a legacy
	// value saved while the field was editable), "base_domain", or "unset".
	HostSource string `json:"host_source"`

	StorageLocked bool `json:"storage_locked"`

	StorageSource string `json:"storage_source"`

	StorageError string `json:"storage_error,omitempty"`
}

func (h *AdminRegistryHandler) view(st *models.RegistrySettings) RegistrySettingsView {
	return RegistrySettingsView{
		RegistrySettings: st,
		EffectiveHost:    h.svc.HostFor(st),
		S3Entitled:       h.ee.Has(enterprise.FlagRegistryS3),
		HostLocked:       true,
		HostSource:       h.svc.HostSource(st),
		StorageLocked:    true,
		StorageSource:    h.svc.StorageSource(st),
		StorageError:     h.svc.StorageUnavailableReason(st),
	}
}

// GetSettings returns the registry settings (secret omitted).
func (h *AdminRegistryHandler) GetSettings(c *okapi.Context) error {
	st, err := h.svc.Get()
	if err != nil {
		return c.AbortInternalServerError("failed to load registry settings", err)
	}
	return ok(c, h.view(st))
}

// UpdateSettings upserts the two mutable registry settings and applies them to
// the container. The storage configuration is not accepted here at all — it is
// environment-only (see UpdateRegistrySettingsRequest), which is also what makes
// the S3 entitlement enforceable: it is checked where the driver is used, at
// container start, not where it used to be selected.
func (h *AdminRegistryHandler) UpdateSettings(c *okapi.Context, req *UpdateRegistrySettingsRequest) error {
	b := req.Body
	if b.PerWorkspaceQuotaMB < 0 {
		return c.AbortBadRequest("the per-workspace quota cannot be negative (0 = unlimited)")
	}
	st, err := h.svc.Save(registryserver.SaveInput{
		DeleteEnabled:       b.DeleteEnabled,
		PerWorkspaceQuotaMB: b.PerWorkspaceQuotaMB,
	})
	if err != nil {
		return c.AbortInternalServerError("failed to save registry settings", err)
	}
	// Apply to the running container (recreate on change / tear down when disabled),
	// best-effort so a Docker hiccup doesn't fail the settings save.
	if h.ensure != nil {
		if err := h.ensure(c.Request().Context()); err != nil {
			logger.Warn("registry ensure after settings change failed", "error", err)
		}
	}
	h.record(c, "registry.settings_update")
	return ok(c, h.view(st))
}

func (h *AdminRegistryHandler) record(c *okapi.Context, action string) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: action, TargetType: "registry", IP: c.RealIP()})
}
