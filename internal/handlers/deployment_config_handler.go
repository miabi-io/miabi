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

// Per-image overrides stay Community; only the mirror is gated.
func (h *DeploymentConfigHandler) mirrorEditable() bool {
	return h.ee != nil && h.ee.Mutable(enterprise.FlagPrivateRegistry)
}

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

// A mirror set under a licence keeps working after it lapses, or an air-gapped install could not start anything.
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

// Trims like Resolver.Mirror so a trailing slash or space is not mistaken for a change.
func normalizeMirror(v string) string {
	return strings.TrimRight(strings.TrimSpace(v), "/")
}

// Only a real change is refused: the admin form round-trips the current mirror on every save.
func mirrorChangeRefused(current, requested string, editable bool) bool {
	return !editable && normalizeMirror(requested) != normalizeMirror(current)
}
