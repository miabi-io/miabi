// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func resizeFixture(t *testing.T, size, used int64) (*Service, *repositories.VolumeRepository, uint) {
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
	v := &models.Volume{UIDModel: models.UIDModel{UID: "u"}, WorkspaceID: 1, Name: "data", DockerName: "d", SizeBytes: size, UsedBytes: used}
	if err := repo.Create(v); err != nil {
		t.Fatal(err)
	}
	return NewService(repo, nil, nil), repo, v.ID
}

func TestResizeOnlyGrows(t *testing.T) {
	tests := []struct {
		name       string
		size, used int64
		to         int64
		want       error
		wantSize   int64
	}{
		{"grow", 100, 0, 200, nil, 200},
		{"same size", 100, 0, 100, nil, 100},
		{"shrink", 100, 0, 50, ErrVolumeShrink, 100},
		{"drop the cap", 100, 0, 0, ErrVolumeShrink, 100},
		{"cap an uncapped volume", 0, 10, 50, nil, 50},
		{"cap below usage", 0, 80, 50, ErrVolumeBelowUsage, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, id := resizeFixture(t, tt.size, tt.used)
			err := svc.Resize(1, id, tt.to)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			v, _ := repo.FindInWorkspace(1, id)
			if v.SizeBytes != tt.wantSize {
				t.Fatalf("size_bytes = %d, want %d", v.SizeBytes, tt.wantSize)
			}
		})
	}
}
