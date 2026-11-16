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

package worker

import (
	"errors"
	"maps"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeClassLookup map[string]*models.StorageClass

func (f fakeClassLookup) FindByName(name string) (*models.StorageClass, error) {
	if c, ok := f[name]; ok {
		return c, nil
	}
	return nil, errors.New("not found")
}

func TestSharedClassVolumeFollowsTheTask(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE volumes (id INTEGER PRIMARY KEY, uid TEXT, workspace_id INTEGER, name TEXT,
		docker_name TEXT, server_id INTEGER, cluster_id INTEGER, driver TEXT, access_mode TEXT, driver_opts_enc TEXT,
		storage_class_name TEXT, host_path TEXT, size_bytes INTEGER, used_bytes INTEGER, metadata TEXT, annotations TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO volumes (id, workspace_id, server_id, docker_name, driver, access_mode, storage_class_name) VALUES
		(1, 1, 11, 'mb-vol-1-data', 'local', 'rwx', 'replicated'),
		(2, 1, 11, 'mb-vol-1-fast', 'local', 'rwo', 'ssd'),
		(3, 1, 11, 'mb-vol-1-gone', 'local', 'rwx', 'unregistered')`)

	h := &DeployHandler{volumes: repositories.NewVolumeRepository(db), poolPolicy: fakePools{}, storageClasses: fakeClassLookup{
		"replicated": {Name: "replicated", Path: "/mnt/shared/miabi", Shared: true},
		"ssd":        {Name: "ssd", Path: "/mnt/ssd1/miabi"},
	}}

	d := h.sharedMountDriver(1, 1)
	want := map[string]string{"type": "none", "o": "bind", "device": "/mnt/shared/miabi/mb-vol-1-data"}
	if d == nil || d.Name != models.VolumeDriverLocal || !maps.Equal(d.Options, want) {
		t.Fatalf("shared class mount driver = %+v, want local bind to %v", d, want["device"])
	}
	if d := h.sharedMountDriver(1, 2); d != nil {
		t.Fatalf("node-local class got a mount driver: %+v", d)
	}
	if d := h.sharedMountDriver(1, 3); d != nil {
		t.Fatalf("volume on an unknown class got a mount driver: %+v", d)
	}

	app := &models.Application{WorkspaceID: 1, Mounts: []models.AppMount{{VolumeID: 1}}}
	if pins := h.volumePins(app); len(pins) != 0 {
		t.Fatalf("shared class volume pinned the service: %v", pins)
	}
}
