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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func sampleRows() []models.AuditLog {
	wsID := uint(7)
	return []models.AuditLog{
		{ID: 1, Action: "app.deploy", TargetType: "app", TargetID: "3", IPAddress: "1.2.3.4", CreatedAt: time.Unix(1700000000, 0).UTC()},
		{ID: 2, WorkspaceID: &wsID, Action: "workspace.role.create", TargetType: "custom_role", TargetID: "9", Metadata: map[string]any{"name": "Deployer"}, CreatedAt: time.Unix(1700000100, 0).UTC()},
	}
}

func onePageFetch(rows []models.AuditLog) func(offset, limit int) ([]models.AuditLog, error) {
	return func(offset, limit int) ([]models.AuditLog, error) {
		if offset == 0 {
			return rows, nil
		}
		return nil, nil
	}
}

func TestStreamJSONValidArray(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := streamJSON(rec, onePageFetch(sampleRows())); err != nil {
		t.Fatal(err)
	}
	var out []models.AuditLog
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("export is not valid JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if len(out) != 2 || out[0].Action != "app.deploy" || out[1].TargetID != "9" {
		t.Fatalf("unexpected rows: %+v", out)
	}
}

func TestStreamCSVRows(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := streamCSV(rec, onePageFetch(sampleRows())); err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("invalid CSV: %v", err)
	}
	if len(records) != 3 { // header + 2 rows
		t.Fatalf("expected header + 2 rows, got %d", len(records))
	}
	if records[0][4] != "action" || records[1][4] != "app.deploy" {
		t.Fatalf("unexpected CSV content: %v", records)
	}
	// Metadata column serialized as JSON for the second row.
	if !strings.Contains(records[2][8], "Deployer") {
		t.Fatalf("metadata not serialized: %q", records[2][8])
	}
}

func TestUintPtrStr(t *testing.T) {
	if uintPtrStr(nil) != "" {
		t.Fatal("nil should be empty")
	}
	v := uint(42)
	if uintPtrStr(&v) != "42" {
		t.Fatal("expected 42")
	}
}
