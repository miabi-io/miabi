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

package repositories

import (
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Stand-in for models.Application, whose Postgres-specific column defaults sqlite can't migrate.
type reconcileAppRow struct {
	ID               uint `gorm:"primaryKey"`
	WorkspaceID      uint
	Name             string
	Status           models.AppStatus
	CurrentReleaseID *uint
}

func (reconcileAppRow) TableName() string { return "applications" }

func newReconcileDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&reconcileAppRow{}, &models.Deployment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestListReconcilableKeepsFailedAppsAndSkipsUnreleasedOnes(t *testing.T) {
	db := newReconcileDB(t)
	rel := uint(1)
	rows := []reconcileAppRow{
		{WorkspaceID: 1, Name: "running", Status: models.AppStatusRunning, CurrentReleaseID: &rel},
		{WorkspaceID: 1, Name: "failed", Status: models.AppStatusFailed, CurrentReleaseID: &rel},
		{WorkspaceID: 1, Name: "stopped", Status: models.AppStatusStopped, CurrentReleaseID: &rel},
		{WorkspaceID: 1, Name: "deploying", Status: models.AppStatusDeploying, CurrentReleaseID: &rel},
		{WorkspaceID: 1, Name: "never-released", Status: models.AppStatusRunning},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := NewApplicationRepository(db).ListReconcilable()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"failed", "running"}) {
		t.Fatalf("ListReconcilable = %v; want [failed running]", names)
	}
}

func TestInProgressAppIDsLeavesOutCanaryAndFinishedDeploys(t *testing.T) {
	deploys := NewDeploymentRepository(newReconcileDB(t))
	for _, d := range []models.Deployment{
		{ApplicationID: 1, Status: models.DeploymentPending},
		{ApplicationID: 1, Status: models.DeploymentDeploying},
		{ApplicationID: 2, Status: models.DeploymentBuilding},
		{ApplicationID: 3, Status: models.DeploymentCanary},
		{ApplicationID: 4, Status: models.DeploymentSucceeded},
		{ApplicationID: 5, Status: models.DeploymentFailed},
	} {
		if err := deploys.Create(&d); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := deploys.InProgressAppIDs()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []uint{1, 2}) {
		t.Fatalf("InProgressAppIDs = %v; want [1 2]", ids)
	}
}
