// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// AdminRegistryHandler exposes the platform's built-in Docker registry settings
// to the super-admin. The registry itself is a Community feature (local storage);
// S3/MinIO storage is gated behind the FlagRegistryS3 entitlement.
type AdminRegistryHandler struct {
	svc   *registryserver.Service
	ee    enterprise.EE
	audit *audit.Logger
	// ensure re-creates/tears down the registry container after a settings change.
	// Injected by the routes layer (it owns the control-plane Docker client).
	ensure  func(context.Context) error
	gc      func(context.Context) error
	runtime func(context.Context) (*registryserver.Runtime, error)
	// repoCount reports how many repositories the registry holds, so a storage
	// change can tell "nothing to strand" from "this abandons real images".
	repoCount func(context.Context) (int, error)
}

// SetRuntime wires the live container status/usage reader.
func (h *AdminRegistryHandler) SetRuntime(fn func(context.Context) (*registryserver.Runtime, error)) {
	h.runtime = fn
}

// SetRepoCount wires the "how many repositories are stored" probe used to decide
// whether a storage change needs confirmation.
func (h *AdminRegistryHandler) SetRepoCount(fn func(context.Context) (int, error)) {
	h.repoCount = fn
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

// UpdateRegistrySettingsRequest is the body for updating the registry settings. The platform
// fields (enablement, hostname, storage) are pointers: nil means "not sent, leave alone", so a
// partial save from one tab cannot clobber another's. Env-pinned fields are ignored.
type UpdateRegistrySettingsRequest struct {
	Body RegistrySettingsBody `json:"body"`
}

// RegistrySettingsBody is the settings payload, named so the confirmation check
// can take it without restating every field.
type RegistrySettingsBody struct {
	DeleteEnabled       bool `json:"delete_enabled"`
	PerWorkspaceQuotaMB int  `json:"per_workspace_quota_mb"`

	Enabled     *bool   `json:"enabled"`
	Host        *string `json:"host"`
	StorageType *string `json:"storage_type"`

	S3Endpoint       *string `json:"s3_endpoint"`
	S3Bucket         *string `json:"s3_bucket"`
	S3Region         *string `json:"s3_region"`
	S3AccessKey      *string `json:"s3_access_key"`
	S3SecretKey      *string `json:"s3_secret_key"` // blank keeps the stored one
	S3ForcePathStyle *bool   `json:"s3_force_path_style"`

	// Confirm acknowledges a change the platform cannot undo — moving the hostname every stored
	// image reference is anchored to, or switching a storage backend that does not migrate its
	// blobs. Without it such a change is refused, so it can never be made by accident.
	Confirm bool `json:"confirm"`
}

// RegistrySettingsView is the settings response enriched with the effective
// host, which fields the environment pins, and why the registry might not be
// serving.
type RegistrySettingsView struct {
	*models.RegistrySettings
	EffectiveHost string `json:"effective_host"`
	S3Entitled    bool   `json:"s3_entitled"`
	// Locks reports the environment-pinned fields. The UI renders each as fixed
	// and names the variable that owns it; everything else is editable.
	Locks registryserver.Locks `json:"locks"`
	// HostSource / StorageSource explain where the effective value came from,
	// which is what the UI shows under a field ("set by MIABI_REGISTRY_HOST",
	// "derived from the base domain", …).
	HostSource    string `json:"host_source"`
	StorageSource string `json:"storage_source"`
	// StorageError is why the registry cannot serve from its configured storage
	// (a missing entitlement or bucket). Empty when storage is usable.
	StorageError string `json:"storage_error,omitempty"`
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

// UpdateSettings saves the registry configuration and applies it to the running container. Two
// changes are refused unless confirmed: moving the hostname (every recorded image reference is
// anchored to it) and switching storage (blobs do not migrate, so pushed images go invisible).
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

	// Selecting S3 is gated here, where the driver is chosen. The service checks
	// it again at container start, which covers the environment path and a licence
	// that lapses later.
	wantsS3 := current.UsesS3()
	if b.StorageType != nil && !locks.Storage {
		wantsS3 = *b.StorageType == models.RegistryStorageS3
	}
	if wantsS3 && !h.ee.Has(enterprise.FlagRegistryS3) {
		return c.AbortWithError(402, errors.New(
			"S3/MinIO storage for the built-in registry requires an Enterprise license (the registry_s3 entitlement)"))
	}

	if msg := h.destructiveChange(c.Request().Context(), current, locks, b); msg != "" && !b.Confirm {
		// 409: the request is well-formed but conflicts with what is already stored.
		// The message is the confirmation prompt the UI shows verbatim.
		return c.AbortWithError(409, errors.New(msg))
	}

	st, err := h.svc.Save(registryserver.SaveInput{
		DeleteEnabled:       b.DeleteEnabled,
		PerWorkspaceQuotaMB: b.PerWorkspaceQuotaMB,
		Enabled:             b.Enabled,
		Host:                b.Host,
		StorageType:         b.StorageType,
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

// destructiveChange returns the confirmation prompt for a change that strands data or breaks
// stored references, or "" when the update is safe. A change is only destructive against
// something that exists: moving the host of a registry that never served costs nothing.
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
	if current.UsesS3() && b.S3Bucket != nil && !locks.S3Bucket {
		if want := strings.TrimSpace(*b.S3Bucket); want != "" && current.S3Bucket != "" && want != current.S3Bucket {
			return "Pointing the registry at bucket " + want + " abandons the " +
				plural(repos, "repository", "repositories") + " stored in " + current.S3Bucket + "."
		}
	}
	return ""
}

// storedRepoCount is the repository count, or 0 when it cannot be determined —
// an unreachable registry is treated as empty so a probe failure can't wedge the
// settings form behind a confirmation nobody can satisfy.
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
