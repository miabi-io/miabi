// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// reconcileAppRow is a sqlite-friendly stand-in for models.Application, whose Postgres-specific column
// defaults sqlite can't migrate. ListReconcilable only filters on status and current_release_id.
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
