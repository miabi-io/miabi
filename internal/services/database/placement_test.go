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

package database

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The real models embed UIDModel, whose gen_random_uuid() default sqlite cannot parse.
type dbInstRow struct {
	ID          uint `gorm:"primaryKey"`
	UID         string
	WorkspaceID uint
	Name        string
	DisplayName string
	Engine      string
	Version     string
	Status      string
	ClusterID   uint
	Metadata    models.Metadata `gorm:"serializer:json"`
	CreatedAt   time.Time
}

func (dbInstRow) TableName() string { return "database_instances" }

type logicalDBRow struct {
	ID            uint `gorm:"primaryKey"`
	UID           string
	WorkspaceID   uint
	InstanceID    uint
	Name          string
	Username      string
	Status        string
	ApplicationID *uint
	Metadata      models.Metadata `gorm:"serializer:json"`
	CreatedAt     time.Time
}

func (logicalDBRow) TableName() string { return "databases" }

// With no rows the Preload("Networks") short-circuits without touching the networks table.
type instNetRow struct {
	DatabaseInstanceID uint `gorm:"primaryKey"`
	NetworkID          uint `gorm:"primaryKey"`
}

func (instNetRow) TableName() string { return "database_instance_networks" }

func newDeclNameSvc(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&dbInstRow{}, &logicalDBRow{}, &instNetRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &Service{repo: repositories.NewDatabaseRepository(db)}, db
}

// A manifest name must resolve to the logical database stamped with it, not the first one on the instance.
func TestFindDatabaseByDeclName(t *testing.T) {
	s, db := newDeclNameSvc(t)
	const ws = uint(1)

	if err := db.Create(&dbInstRow{
		ID: 1, UID: "inst-1", WorkspaceID: ws, Name: "shared-pg", DisplayName: "shared-pg",
		Engine: string(models.DBEnginePostgres), Version: "17", Status: string(models.DBStatusRunning),
	}).Error; err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	rows := []logicalDBRow{
		{
			ID: 1, UID: "db-authentik", WorkspaceID: ws, InstanceID: 1, Name: "authentik_db",
			Username: "u_authentik", Status: string(models.DBStatusRunning), CreatedAt: time.Unix(1000, 0),
		},
		{
			ID: 2, UID: "db-posta", WorkspaceID: ws, InstanceID: 1, Name: "posta_db",
			Username: "u_posta", Status: string(models.DBStatusRunning),
			CreatedAt: time.Unix(2000, 0), // newer → would be ListDatabases dbs[0] (DESC)
			Metadata:  models.SetBuiltin(models.Metadata{}, models.MetaDeclarativeName, "posta-db"),
		},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed databases: %v", err)
	}

	got, gotInst, ok := s.FindDatabaseByDeclName(ws, "posta-db")
	if !ok {
		t.Fatal("FindDatabaseByDeclName(posta-db): not found")
	}
	if got.Name != "posta_db" {
		t.Errorf("resolved logical db = %q, want posta_db (must not be authentik_db)", got.Name)
	}
	if gotInst == nil || gotInst.ID != 1 {
		t.Errorf("resolved instance = %v, want id 1", gotInst)
	}

	// An untagged database is never resolved by a declarative name.
	if _, _, ok := s.FindDatabaseByDeclName(ws, "authentik-db"); ok {
		t.Error("FindDatabaseByDeclName(authentik-db): unexpectedly matched an untagged database")
	}
	if _, _, ok := s.FindDatabaseByDeclName(ws, ""); ok {
		t.Error("FindDatabaseByDeclName(\"\"): empty name must never match")
	}
}

// Reuse never crosses a location: an instance in another cluster is unreachable from the install's network.
func TestFindReusableInstanceStaysInTheLocation(t *testing.T) {
	s, db := newDeclNameSvc(t)
	const ws = uint(1)
	running, pg := string(models.DBStatusRunning), string(models.DBEnginePostgres)
	if err := db.Create(&[]dbInstRow{
		{ID: 1, UID: "pg-1", WorkspaceID: ws, Name: "pg-central", Engine: pg, Version: "17", Status: running, ClusterID: 1},
		{ID: 2, UID: "pg-2", WorkspaceID: ws, Name: "pg-east", Engine: pg, Version: "17", Status: running, ClusterID: 2},
	}).Error; err != nil {
		t.Fatalf("seed instances: %v", err)
	}

	if got := s.FindReusableInstance(ws, models.DBEnginePostgres, "", 2); got == nil || got.ID != 2 {
		t.Errorf("location 2 reused %v, want pg-east", got)
	}
	if got := s.FindReusableInstance(ws, models.DBEnginePostgres, "", 3); got != nil {
		t.Errorf("a location with no postgres reused %q", got.Name)
	}
	if got := s.FindReusableInstance(ws, models.DBEnginePostgres, "", 0); got == nil {
		t.Error("an unscoped search found nothing")
	}
}

// A declared version is part of compatibility: a database never lands on an instance running another one.
func TestFindReusableInstanceMatchesTheVersion(t *testing.T) {
	s, db := newDeclNameSvc(t)
	const ws = uint(1)
	running, pg := string(models.DBStatusRunning), string(models.DBEnginePostgres)
	if err := db.Create(&[]dbInstRow{
		{ID: 1, UID: "pg-16", WorkspaceID: ws, Name: "pg-16", Engine: pg, Version: "16", Status: running, ClusterID: 1},
		{ID: 2, UID: "pg-17", WorkspaceID: ws, Name: "pg-17", Engine: pg, Version: "17", Status: running, ClusterID: 1},
	}).Error; err != nil {
		t.Fatalf("seed instances: %v", err)
	}

	if got := s.FindReusableInstance(ws, models.DBEnginePostgres, "17", 0); got == nil || got.ID != 2 {
		t.Errorf("version 17 reused %v, want pg-17", got)
	}
	if got := s.FindReusableInstance(ws, models.DBEnginePostgres, "15", 0); got != nil {
		t.Errorf("version 15 reused %q, which runs %s", got.Name, got.Version)
	}
	if got := s.FindReusableInstance(ws, models.DBEnginePostgres, "", 0); got == nil {
		t.Error("an unstated version found nothing")
	}
}

// An instance provisioned as dedicated serves only its own dependency; later auto/shared ones must look elsewhere.
func TestFindReusableInstanceSkipsDedicated(t *testing.T) {
	s, db := newDeclNameSvc(t)
	const ws = uint(1)
	running, pg := string(models.DBStatusRunning), string(models.DBEnginePostgres)
	if err := db.Create(&dbInstRow{
		ID: 1, UID: "pg-a", WorkspaceID: ws, Name: "a-db", Engine: pg, Version: "17", Status: running,
		Metadata: dedicatedMeta(nil),
	}).Error; err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	if got := s.FindReusableInstance(ws, models.DBEnginePostgres, "17", 0); got != nil {
		t.Fatalf("reused dedicated instance %q", got.Name)
	}

	if err := db.Create(&dbInstRow{
		ID: 2, UID: "pg-b", WorkspaceID: ws, Name: "shared-pg", Engine: pg, Version: "17", Status: running,
	}).Error; err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	if got := s.FindReusableInstance(ws, models.DBEnginePostgres, "17", 0); got == nil || got.ID != 2 {
		t.Errorf("reused %v, want shared-pg", got)
	}
}

func TestDedicatedMetaLeavesTheCallersMapAlone(t *testing.T) {
	meta := models.Metadata{models.MetaManagedBy: models.ManagedByApply}
	out := dedicatedMeta(meta)
	if out[models.MetaPlacement] != models.PlacementValueDedicated || out[models.MetaManagedBy] != models.ManagedByApply {
		t.Fatalf("dedicatedMeta = %v", out)
	}
	if _, ok := meta[models.MetaPlacement]; ok {
		t.Fatal("the caller's metadata was stamped; it also tags the logical database")
	}
}

// A dedicated instance serves one app: a second app may not take a database on it, the app it serves may.
func TestCheckDedicatedAttach(t *testing.T) {
	s, db := newDeclNameSvc(t)
	const ws = uint(1)
	served := uint(7)
	running, pg := string(models.DBStatusRunning), string(models.DBEnginePostgres)
	if err := db.Create(&logicalDBRow{ID: 1, UID: "d1", WorkspaceID: ws, InstanceID: 1, Name: "a", ApplicationID: &served}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&logicalDBRow{ID: 2, UID: "d2", WorkspaceID: ws, InstanceID: 1, Name: "b"}).Error; err != nil {
		t.Fatal(err)
	}
	inst := &models.DatabaseInstance{ID: 1, WorkspaceID: ws, Engine: models.DBEngine(pg), Status: models.DBStatus(running), Metadata: dedicatedMeta(nil)}

	if err := s.CheckDedicatedAttach(inst, 8); !errors.Is(err, ErrInstanceDedicated) {
		t.Fatalf("second app: err = %v; want ErrInstanceDedicated", err)
	}
	if err := s.CheckDedicatedAttach(inst, served); err != nil {
		t.Fatalf("the app it serves: %v", err)
	}
	inst.Metadata = nil
	if err := s.CheckDedicatedAttach(inst, 8); err != nil {
		t.Fatalf("shared instance: %v", err)
	}
}
