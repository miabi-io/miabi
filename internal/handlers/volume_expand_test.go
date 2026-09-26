// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func expandFixture(t *testing.T) (*okapi.Okapi, *repositories.VolumeRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// The uid column defaults to the Postgres-only gen_random_uuid(), so the table is created by hand.
	err = db.Exec(`CREATE TABLE volumes (
		id INTEGER PRIMARY KEY AUTOINCREMENT, uid TEXT, workspace_id INTEGER, name TEXT, display_name TEXT,
		docker_name TEXT, server_id INTEGER DEFAULT 0, cluster_id INTEGER DEFAULT 0, mountpoint TEXT,
		engine_created_at TEXT, size_bytes INTEGER DEFAULT 0, used_bytes INTEGER DEFAULT 0, used_measured_at datetime,
		imported NUMERIC DEFAULT 0, driver TEXT, access_mode TEXT, driver_opts_enc TEXT, storage_class_name TEXT,
		host_path TEXT, metadata TEXT, annotations TEXT, created_at datetime, updated_at datetime)`).Error
	if err != nil {
		t.Fatal(err)
	}
	repo := repositories.NewVolumeRepository(db)
	h := NewVolumeHandler(storage.NewService(repo, nil, nil), nil, nil)
	app := okapi.New()
	app.Post("/workspaces/{workspace}/volumes/{volumeID}/expand", okapi.H(h.Expand), okapi.UseMiddleware(func(c *okapi.Context) error {
		c.Set(middlewares.CtxWorkspaceID, 1)
		return c.Next()
	}))
	return app, repo
}

func TestVolumeExpand(t *testing.T) {
	const mb = int64(1024 * 1024)
	tests := []struct {
		name      string
		size      int64
		body      string
		wantCode  int
		wantBytes int64
	}{
		{"grows", 100 * mb, `{"size_mb":200}`, http.StatusOK, 200 * mb},
		{"same size", 100 * mb, `{"size_mb":100}`, http.StatusBadRequest, 100 * mb},
		{"shrink", 100 * mb, `{"size_mb":50}`, http.StatusBadRequest, 100 * mb},
		{"uncapped volume", 0, `{"size_mb":200}`, http.StatusBadRequest, 0},
		{"zero", 100 * mb, `{"size_mb":0}`, http.StatusBadRequest, 100 * mb},
		{"missing size", 100 * mb, `{}`, http.StatusBadRequest, 100 * mb},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, repo := expandFixture(t)
			v := &models.Volume{UIDModel: models.UIDModel{UID: "u-" + tt.name}, WorkspaceID: 1, Name: "data", DockerName: "d-" + tt.name, SizeBytes: tt.size}
			if err := repo.Create(v); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "/workspaces/1/volumes/1/expand", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, r)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			got, err := repo.FindInWorkspace(1, v.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.SizeBytes != tt.wantBytes {
				t.Fatalf("size_bytes = %d, want %d", got.SizeBytes, tt.wantBytes)
			}
		})
	}
}
