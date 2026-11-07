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
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/siem"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// The in-app audit log stays open-source; only external streaming is gated.
type SIEMAdminHandler struct {
	repo     *repositories.SIEMConfigRepository
	streamer *siem.Streamer
	ee       enterprise.EE
	audit    *audit.Logger
}

func NewSIEMAdminHandler(repo *repositories.SIEMConfigRepository, streamer *siem.Streamer, ee enterprise.EE, auditLog *audit.Logger) *SIEMAdminHandler {
	return &SIEMAdminHandler{repo: repo, streamer: streamer, ee: ee, audit: auditLog}
}

type SIEMConfigRequest struct {
	Body struct {
		Name       string `json:"name" required:"true"`
		Sink       string `json:"sink" required:"true" enum:"syslog,webhook"`
		Endpoint   string `json:"endpoint" required:"true"`
		Format     string `json:"format" enum:"json,cef"`
		AuthHeader string `json:"auth_header"` // webhook Authorization value; write-only
		Enabled    *bool  `json:"enabled"`
	} `json:"body"`
}

func (h *SIEMAdminHandler) List(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagSIEMStream); err != nil {
		return entitlementAbort(c, err)
	}
	configs, err := h.repo.FindAll()
	if err != nil {
		return c.AbortInternalServerError("failed to list SIEM targets", err)
	}
	return ok(c, configs)
}

func (h *SIEMAdminHandler) Create(c *okapi.Context, req *SIEMConfigRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagSIEMStream); err != nil {
		return entitlementAbort(c, err)
	}
	if !models.IsValidSIEMSink(req.Body.Sink) || req.Body.Sink == models.SIEMSinkS3 {
		return c.AbortBadRequest("sink must be syslog or webhook")
	}
	cfg := &models.SIEMConfig{
		Name:     strings.TrimSpace(req.Body.Name),
		Sink:     req.Body.Sink,
		Endpoint: strings.TrimSpace(req.Body.Endpoint),
		Format:   orStr(req.Body.Format, models.SIEMFormatJSON),
		Enabled:  boolOr(req.Body.Enabled, true),
	}
	if v := strings.TrimSpace(req.Body.AuthHeader); v != "" {
		enc, err := crypto.Encrypt(v)
		if err != nil {
			return c.AbortInternalServerError("failed to encrypt auth header", err)
		}
		cfg.AuthHeaderEnc = enc
	}
	if err := h.repo.Create(cfg); err != nil {
		return c.AbortInternalServerError("failed to create SIEM target", err)
	}
	h.record(c, "admin.siem.create", cfg.ID)
	return created(c, cfg)
}

func (h *SIEMAdminHandler) Update(c *okapi.Context, req *SIEMConfigRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagSIEMStream); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	cfg, err := h.repo.FindByID(id)
	if err != nil {
		return c.AbortNotFound("SIEM target not found")
	}
	if v := strings.TrimSpace(req.Body.Name); v != "" {
		cfg.Name = v
	}
	if req.Body.Sink != "" {
		if !models.IsValidSIEMSink(req.Body.Sink) || req.Body.Sink == models.SIEMSinkS3 {
			return c.AbortBadRequest("sink must be syslog or webhook")
		}
		cfg.Sink = req.Body.Sink
	}
	if v := strings.TrimSpace(req.Body.Endpoint); v != "" {
		cfg.Endpoint = v
	}
	if v := strings.TrimSpace(req.Body.Format); v != "" {
		cfg.Format = v
	}
	if req.Body.Enabled != nil {
		cfg.Enabled = *req.Body.Enabled
	}
	// A blank auth_header preserves the stored secret; a value replaces it.
	if v := strings.TrimSpace(req.Body.AuthHeader); v != "" {
		enc, err := crypto.Encrypt(v)
		if err != nil {
			return c.AbortInternalServerError("failed to encrypt auth header", err)
		}
		cfg.AuthHeaderEnc = enc
	}
	if err := h.repo.Save(cfg); err != nil {
		return c.AbortInternalServerError("failed to update SIEM target", err)
	}
	h.record(c, "admin.siem.update", cfg.ID)
	return ok(c, cfg)
}

func (h *SIEMAdminHandler) Delete(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagSIEMStream); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	if err := h.repo.Delete(id); err != nil {
		return c.AbortInternalServerError("failed to delete SIEM target", err)
	}
	h.record(c, "admin.siem.delete", id)
	return message(c, "SIEM target deleted")
}

// Test ships a synthetic event to verify connectivity, surfacing the sink error.
func (h *SIEMAdminHandler) Test(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagSIEMStream); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	cfg, err := h.repo.FindByID(id)
	if err != nil {
		return c.AbortNotFound("SIEM target not found")
	}
	actor := middlewares.UserID(c)
	event := models.AuditLog{
		ID: 0, ActorID: &actor, Action: "siem.test",
		TargetType: "siem_config", TargetID: "test", IPAddress: c.RealIP(),
		Metadata: map[string]any{"message": "Miabi SIEM connectivity test"}, CreatedAt: time.Now(),
	}
	if err := h.streamer.Test(cfg, event); err != nil {
		return c.AbortWithError(502, err)
	}
	return message(c, "test event delivered")
}

func (h *SIEMAdminHandler) record(c *okapi.Context, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: action, TargetType: "siem_config",
		TargetID: strconv.Itoa(int(id)), IP: c.RealIP(),
	})
}
