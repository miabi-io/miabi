// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type dedClusterRow struct {
	ID             uint `gorm:"primaryKey"`
	Name           string
	OrganizationID *uint
}

func (dedClusterRow) TableName() string { return "clusters" }

type dedWorkspaceRow struct {
	ID               uint `gorm:"primaryKey"`
	Name             string
	OrganizationID   *uint
	DefaultClusterID *uint
}

func (dedWorkspaceRow) TableName() string { return "workspaces" }

// A workspace keeps the default location it last chose. Dedicating a cluster can make that a
// location it may no longer place in — from either side — and this step clears exactly those,
// leaving every still-valid default alone.
func TestClusterDedicationClearsUnusableDefaults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&dedClusterRow{}, &dedWorkspaceRow{}); err != nil {
		t.Fatal(err)
	}
	// Cluster 1 shared, 2 dedicated to org 7, 3 dedicated to org 8.
	if err := db.Create(&[]dedClusterRow{
		{ID: 1, Name: "default"},
		{ID: 2, Name: "acme", OrganizationID: u(7)},
		{ID: 3, Name: "globex", OrganizationID: u(8)},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]dedWorkspaceRow{
		// Org 7 owns cluster 2, so it is confined: a shared default is no longer usable.
		{ID: 1, Name: "acme-on-shared", OrganizationID: u(7), DefaultClusterID: u(1)},
		// ...and its own cluster stays.
		{ID: 2, Name: "acme-on-own", OrganizationID: u(7), DefaultClusterID: u(2)},
		// Org 9 owns nothing, but points at somebody else's cluster.
		{ID: 3, Name: "outsider-on-dedicated", OrganizationID: u(9), DefaultClusterID: u(3)},
		// Org 9 on a shared cluster is fine — it is not confined.
		{ID: 4, Name: "outsider-on-shared", OrganizationID: u(9), DefaultClusterID: u(1)},
		// No default at all, nothing to clear.
		{ID: 5, Name: "no-default", OrganizationID: u(9)},
	}).Error; err != nil {
		t.Fatal(err)
	}

	if err := clusterDedicationStep(context.Background(), db); err != nil {
		t.Fatalf("step: %v", err)
	}

	want := map[uint]*uint{1: nil, 2: u(2), 3: nil, 4: u(1), 5: nil}
	for id, expected := range want {
		var got dedWorkspaceRow
		if err := db.First(&got, id).Error; err != nil {
			t.Fatal(err)
		}
		switch {
		case expected == nil && got.DefaultClusterID != nil:
			t.Errorf("workspace %d (%s): default = %d, want cleared", id, got.Name, *got.DefaultClusterID)
		case expected != nil && (got.DefaultClusterID == nil || *got.DefaultClusterID != *expected):
			t.Errorf("workspace %d (%s): default = %v, want %d", id, got.Name, got.DefaultClusterID, *expected)
		}
	}

	// Idempotent: a second run has nothing left to do.
	if err := clusterDedicationStep(context.Background(), db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var stillSet dedWorkspaceRow
	if err := db.First(&stillSet, 2).Error; err != nil {
		t.Fatal(err)
	}
	if stillSet.DefaultClusterID == nil || *stillSet.DefaultClusterID != 2 {
		t.Error("a valid default must survive a second run")
	}
}
