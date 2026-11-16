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

package apply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type namedClasses map[string]*models.StorageClass

func (n namedClasses) Resolve(_ uint, name string) (*models.StorageClass, error) {
	if c, ok := n[name]; ok {
		return c, nil
	}
	return nil, errors.New("storage class not found: " + name)
}
func (namedClasses) EnsureDir(context.Context, *models.StorageClass, string) error { return nil }
func (namedClasses) RemoveDir(context.Context, *models.StorageClass, string) error { return nil }
func (namedClasses) DiskUsageAll(context.Context, *models.StorageClass) (map[string]int64, error) {
	return nil, nil
}
func (namedClasses) ListManaged() ([]models.StorageClass, error) { return nil, nil }

func TestPlanAllowsReplicasOnAVolumeDeclaredOnASharedClass(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE volumes (id INTEGER PRIMARY KEY, workspace_id INTEGER, name TEXT,
		created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	st := storage.NewService(repositories.NewVolumeRepository(db), nil, nil)
	st.SetStorageClasses(namedClasses{
		"replicated": {Name: "replicated", Path: "/mnt/shared/miabi", Shared: true, Enabled: true},
		"ssd":        {Name: "ssd", Path: "/mnt/ssd1/miabi", Enabled: true},
	})
	s := &Service{
		storage: st,
		placer:  fakePlacer{clusters: map[string]*models.Cluster{"eu": {ID: 1, Name: "eu"}}},
		cluster: fakeCap{swarm: map[uint]bool{1: true}},
	}
	check := func(class string) error {
		volume := strings.TrimSuffix(dataVolume("eu"), "---\n") + "  storageClass: " + class + "\n---\n"
		manifest := volume + webApp + located("eu") + "  deployment:\n    runtime: service\n    replicas: 2\n" + mountsData
		desired, err := declarative.Parse([]byte(manifest))
		if err != nil {
			t.Fatal(err)
		}
		plan := declarative.BuildPlan(desired, declarative.NewResourceSet(), declarative.PlanOptions{})
		return s.checkLocations(7, plan, desired, false)
	}
	if err := check("replicated"); err != nil {
		t.Fatalf("shared class: %v", err)
	}
	if err := check("ssd"); !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), "node-local volume") {
		t.Fatalf("node-local class: err = %v, want the replicated mount refused", err)
	}
}
