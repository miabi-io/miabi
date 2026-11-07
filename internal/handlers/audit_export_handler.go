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
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

const auditExportBatch = 1000

type AuditExportHandler struct {
	repo *repositories.AuditLogRepository
	ee   enterprise.EE
}

func NewAuditExportHandler(repo *repositories.AuditLogRepository, ee enterprise.EE) *AuditExportHandler {
	return &AuditExportHandler{repo: repo, ee: ee}
}

// AdminExport streams the platform-wide audit log (system admin).
func (h *AuditExportHandler) AdminExport(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagAuditExport); err != nil {
		return entitlementAbort(c, err)
	}
	from, to := timeRange(c)
	action, search := c.Query("action"), c.Query("search")
	fetch := func(offset, limit int) ([]models.AuditLog, error) {
		rows, _, err := h.repo.FindAll(search, action, "asc", from, to, limit, offset)
		return rows, err
	}
	return streamAudit(c, c.Query("format"), "audit-platform", fetch)
}

// WorkspaceExport streams a single workspace's audit log (workspace admin).
func (h *AuditExportHandler) WorkspaceExport(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagAuditExport); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	from, to := timeRange(c)
	fetch := func(offset, limit int) ([]models.AuditLog, error) {
		rows, _, err := h.repo.ListByWorkspace(wsID, "asc", from, to, limit, offset)
		return rows, err
	}
	return streamAudit(c, c.Query("format"), "audit-workspace", fetch)
}

// Once streaming begins, a mid-stream DB error ends the output: the response is already committed.
func streamAudit(c *okapi.Context, format, name string, fetch func(offset, limit int) ([]models.AuditLog, error)) error {
	w := c.ResponseWriter()
	if strings.EqualFold(format, "csv") {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
		w.WriteHeader(http.StatusOK)
		return streamCSV(w, fetch)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
	w.WriteHeader(http.StatusOK)
	return streamJSON(w, fetch)
}

func streamJSON(w http.ResponseWriter, fetch func(offset, limit int) ([]models.AuditLog, error)) error {
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	_, _ = w.Write([]byte("["))
	first := true
	for offset := 0; ; offset += auditExportBatch {
		rows, err := fetch(offset, auditExportBatch)
		if err != nil {
			break
		}
		for i := range rows {
			if !first {
				_, _ = w.Write([]byte(","))
			}
			first = false
			_ = enc.Encode(&rows[i]) // object + newline; whitespace is valid in a JSON array
		}
		if flusher != nil {
			flusher.Flush()
		}
		if len(rows) < auditExportBatch {
			break
		}
	}
	_, _ = w.Write([]byte("]"))
	return nil
}

func streamCSV(w http.ResponseWriter, fetch func(offset, limit int) ([]models.AuditLog, error)) error {
	flusher, _ := w.(http.Flusher)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "created_at", "actor_id", "workspace_id", "action", "target_type", "target_id", "ip_address", "metadata"})
	for offset := 0; ; offset += auditExportBatch {
		rows, err := fetch(offset, auditExportBatch)
		if err != nil {
			break
		}
		for i := range rows {
			e := &rows[i]
			meta := ""
			if len(e.Metadata) > 0 {
				if b, err := json.Marshal(e.Metadata); err == nil {
					meta = string(b)
				}
			}
			_ = cw.Write([]string{
				strconv.FormatUint(uint64(e.ID), 10),
				e.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
				uintPtrStr(e.ActorID), uintPtrStr(e.WorkspaceID),
				e.Action, e.TargetType, e.TargetID, e.IPAddress, meta,
			})
		}
		cw.Flush()
		if flusher != nil {
			flusher.Flush()
		}
		if len(rows) < auditExportBatch {
			break
		}
	}
	cw.Flush()
	return nil
}

func uintPtrStr(p *uint) string {
	if p == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*p), 10)
}
