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
	"fmt"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakePools struct{}

func (fakePools) PoolConstraints(uint, uint) []string { return []string{"node.labels.miabi.pool==pro"} }
func (fakePools) NodeConstraint(id uint) string {
	if id == 99 {
		return ""
	}
	return fmt.Sprintf("node.id==sw-%d", id)
}

func TestServiceConstraintsPinNodeLocalVolumes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// The uid column defaults to the Postgres-only gen_random_uuid(), so the table is created by hand.
	if err := db.Exec(`CREATE TABLE volumes (id INTEGER PRIMARY KEY, uid TEXT, workspace_id INTEGER, name TEXT,
		docker_name TEXT, server_id INTEGER, cluster_id INTEGER, driver TEXT, access_mode TEXT, driver_opts_enc TEXT,
		storage_class_name TEXT, host_path TEXT, size_bytes INTEGER, used_bytes INTEGER, metadata TEXT, annotations TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO volumes (id, workspace_id, server_id, driver, access_mode) VALUES
		(1, 1, 11, 'local', 'rwo'), (2, 1, 11, 'local', 'rwo'), (3, 1, 12, 'nfs', 'rwx'), (4, 1, 12, 'host', 'rwx'), (5, 1, 99, 'local', 'rwo')`)

	h := &DeployHandler{volumes: repositories.NewVolumeRepository(db), poolPolicy: fakePools{}}
	app := &models.Application{WorkspaceID: 1, PlacementConstraints: []string{"node.role==worker"}, Mounts: []models.AppMount{
		{VolumeID: 1}, {VolumeID: 2}, {VolumeID: 3}, {VolumeID: 4}, {VolumeID: 5}, {HostPreset: "docker-socket"},
	}}
	got := h.serviceConstraints(app)
	want := []string{"node.role==worker", "node.labels.miabi.pool==pro", "node.id==sw-11"}
	if !slices.Equal(got, want) {
		t.Fatalf("constraints = %v, want %v", got, want)
	}
}
