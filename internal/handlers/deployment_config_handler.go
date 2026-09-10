// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// DeploymentConfigHandler is the admin Deployment Config: the catalog of every
// image the platform runs, with per-image overrides and a global registry mirror.
type DeploymentConfigHandler struct {
	resolver *platformimage.Resolver
	repo     *repositories.SettingRepository
	provider *settings.Provider
	audit    *audit.Logger
	ee       enterprise.EE
}

func NewDeploymentConfigHandler(resolver *platformimage.Resolver, repo *repositories.SettingRepository, provider *settings.Provider, auditLog *audit.Logger, ee enterprise.EE) *DeploymentConfigHandler {
	return &DeploymentConfigHandler{resolver: resolver, repo: repo, provider: provider, audit: auditLog, ee: ee}
}

// mirrorEditable reports whether this licence may change the registry mirror.
// Per-image overrides stay Community; only the mirror is gated.
func (h *DeploymentConfigHandler) mirrorEditable() bool {
	return h.ee != nil && h.ee.Mutable(enterprise.FlagPrivateRegistry)
}

// Get returns the image catalog (default / override / effective per image) and
// the registry mirror.
func (h *DeploymentConfigHandler) Get(c *okapi.Context) error {
	return ok(c, map[string]any{
		"images":          h.resolver.Catalog(),
		"mirror":          h.resolver.Mirror(),
		"mirror_editable": h.mirrorEditable(),
	})
}

type UpdateDeploymentConfigRequest struct {
	Body struct {
		// Mirror is the global registry mirror/prefix (empty = none).
		Mirror string `json:"mirror"`
		// Images maps catalog keys to override refs ("" clears the override).
		Images map[string]string `json:"images"`
	} `json:"body"`
}

// Update writes image overrides + the mirror. Unknown keys are ignored.
//
// The registry mirror needs the private_registry entitlement to CHANGE, not to
// keep. A mirror set under a licence goes on working after it lapses — an
// air-gapped platform that suddenly resolved every image to an unreachable
// registry would stop being able to start anything, which is not a licensing
// outcome worth defending. Per-image overrides are unaffected and stay Community.
func (h *DeploymentConfigHandler) Update(c *okapi.Context, req *UpdateDeploymentConfigRequest) error {
	mirror := normalizeMirror(req.Body.Mirror)
	if mirrorChangeRefused(h.resolver.Mirror(), mirror, h.mirrorEditable()) {
		return c.AbortWithError(402, enterprise.ErrLicenseRequired)
	}
	toSave := []models.Setting{
		{Key: platformimage.MirrorSettingKey(), Value: mirror, Type: models.SettingTypeString},
	}
	for key, val := range req.Body.Images {
		if !h.resolver.ValidKey(key) {
			continue
		}
		toSave = append(toSave, models.Setting{Key: platformimage.SettingKey(key), Value: val, Type: models.SettingTypeString})
	}
	if err := h.repo.BulkUpsert(toSave); err != nil {
		return c.AbortInternalServerError("failed to save deployment config", err)
	}
	h.provider.Invalidate()
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: "admin.deployment_config.update", TargetType: "deployment_config", IP: c.RealIP(), Metadata: map[string]any{"count": len(toSave)}})
	return h.Get(c)
}

// normalizeMirror trims the value the way Resolver.Mirror reports it, so a
// trailing slash or stray space is not mistaken for a change.
func normalizeMirror(v string) string {
	return strings.TrimRight(strings.TrimSpace(v), "/")
}

// mirrorChangeRefused reports whether this request tries to change the registry
// mirror without the entitlement to.
//
// Only a real change is refused. The admin form round-trips the current mirror on
// every save, so refusing on presence rather than on difference would stop a
// Community admin editing the per-image overrides that are theirs to edit.
func mirrorChangeRefused(current, requested string, editable bool) bool {
	return !editable && normalizeMirror(requested) != normalizeMirror(current)
}
