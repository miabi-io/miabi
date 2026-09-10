// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/registration"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/storage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

const reservedSettingPrefix = "app."

// reservedSettingPrefixes are key prefixes managed by dedicated screens (not the
// generic settings list): app.* (internal) and image.* (Deployment Config).
var reservedSettingPrefixes = []string{reservedSettingPrefix, "image."}

func isReservedSetting(key string) bool {
	for _, p := range reservedSettingPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// readOnlySettingKeys are system-managed: they appear in the settings list so an admin can see
// them, but Update silently skips them, so they can never be changed from the dashboard or API.
// install_id is the stable deployment identity — rewriting it would break license binding.
var readOnlySettingKeys = map[string]bool{
	storage.InstallIDKey: true,
}

// AdminSettingHandler exposes platform-wide settings (super-admin only).
type AdminSettingHandler struct {
	repo     *repositories.SettingRepository
	provider *settings.Provider
	audit    *audit.Logger
	// reg and passwordReset back AuthAccess. Both are fixed at boot rather than
	// stored, so they are reported, never written.
	reg           *registration.Service
	passwordReset bool
}

func NewAdminSettingHandler(repo *repositories.SettingRepository, provider *settings.Provider, auditLog *audit.Logger) *AdminSettingHandler {
	return &AdminSettingHandler{repo: repo, provider: provider, audit: auditLog}
}

// SetAuthAccess wires the env-fixed auth controls that AuthAccess reports.
func (h *AdminSettingHandler) SetAuthAccess(reg *registration.Service, passwordReset bool) {
	h.reg, h.passwordReset = reg, passwordReset
}

// AuthAccessStatus reports the auth controls an admin cannot change from here,
// so the settings screen can show them beside the ones they can.
type AuthAccessStatus struct {
	RegistrationEnabled  bool `json:"registration_enabled"`
	PasswordResetEnabled bool `json:"password_reset_enabled"`
	// Blocked explains a sign-up that is switched on but cannot be offered, which
	// is otherwise indistinguishable from one that was never switched on. Empty
	// when there is nothing to explain.
	Blocked string `json:"blocked,omitempty"`
}

// AuthAccess reports the env-fixed registration and password-reset controls.
func (h *AdminSettingHandler) AuthAccess(c *okapi.Context) error {
	st := AuthAccessStatus{PasswordResetEnabled: h.passwordReset}
	if h.reg != nil {
		st.RegistrationEnabled = h.reg.Enabled()
		if err := h.reg.Available(); err != nil && st.RegistrationEnabled {
			st.Blocked = err.Error()
		}
	}
	return ok(c, st)
}

type UpdateSettingsRequest struct {
	Body struct {
		Settings []struct {
			Key   string `json:"key" required:"true"`
			Value string `json:"value"`
			Type  string `json:"type" enum:"string,int,bool,json"`
		} `json:"settings" required:"true"`
	} `json:"body"`
}

// List returns all non-reserved settings.
func (h *AdminSettingHandler) List(c *okapi.Context) error {
	all, err := h.repo.All()
	if err != nil {
		return c.AbortInternalServerError("failed to list settings", err)
	}
	out := make([]models.Setting, 0, len(all))
	for _, s := range all {
		if isReservedSetting(s.Key) {
			continue
		}
		// An env-supplied key is re-forced on every boot, so the console shows it
		// read-only rather than offering an edit that silently reverts on restart.
		s.Pinned = h.provider != nil && h.provider.Pinned(s.Key)
		out = append(out, s)
	}
	return ok(c, out)
}

// Update upserts the supplied settings and refreshes the cache.
func (h *AdminSettingHandler) Update(c *okapi.Context, req *UpdateSettingsRequest) error {
	if len(req.Body.Settings) == 0 {
		return c.AbortBadRequest("no settings supplied")
	}
	toSave := make([]models.Setting, 0, len(req.Body.Settings))
	for _, s := range req.Body.Settings {
		key := strings.TrimSpace(s.Key)
		if key == "" || isReservedSetting(key) {
			return c.AbortBadRequest("invalid or reserved setting key")
		}
		// System-managed keys (e.g. install_id) are shown but never editable; the
		// UI resubmits every key, so skip rather than reject.
		if readOnlySettingKeys[key] {
			continue
		}
		// Same reasoning as readOnlySettingKeys: the UI resubmits every key, so an
		// env-pinned one is skipped rather than rejected. Writing it would be
		// pointless anyway — the next boot overwrites it from the environment.
		if h.provider != nil && h.provider.Pinned(key) {
			continue
		}
		t := models.SettingType(s.Type)
		if t == "" {
			t = models.SettingTypeString
		}
		toSave = append(toSave, models.Setting{Key: key, Value: s.Value, Type: t})
	}
	if err := h.repo.BulkUpsert(toSave); err != nil {
		return c.AbortInternalServerError("failed to save settings", err)
	}
	h.provider.Invalidate()

	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: "admin.settings.update", TargetType: "settings",
		IP: c.RealIP(), Metadata: map[string]any{"count": len(toSave)},
	})
	return h.List(c)
}
