// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
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
}

func NewAdminSettingHandler(repo *repositories.SettingRepository, provider *settings.Provider, auditLog *audit.Logger) *AdminSettingHandler {
	return &AdminSettingHandler{repo: repo, provider: provider, audit: auditLog}
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
