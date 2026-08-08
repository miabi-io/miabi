// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package account

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The production models carry Postgres-specific column defaults sqlite can't migrate. The service and
// repositories only ever SELECT, delete and update these tables, so the test creates a minimal sqlite-friendly
// schema under the same table names and lets the real repos query it; missing model fields stay zero.

type wsRow struct {
	ID        uint `gorm:"primaryKey"`
	OwnerID   uint
	CreatedAt time.Time
}

func (wsRow) TableName() string { return "workspaces" }

type memberRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	UserID      uint
	Role        string
}

func (memberRow) TableName() string { return "workspace_members" }

type inviteRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
}

func (inviteRow) TableName() string { return "workspace_invitations" }

type appRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	Status      string
	ServerID    uint
	CreatedAt   time.Time
}

func (appRow) TableName() string { return "applications" }

type instRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	Status      string
	CreatedAt   time.Time
}

func (instRow) TableName() string { return "database_instances" }

type ldbRow struct {
	ID            uint `gorm:"primaryKey"`
	WorkspaceID   uint
	InstanceID    uint
	ApplicationID *uint
	CreatedAt     time.Time
}

func (ldbRow) TableName() string { return "databases" }

type stackRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	CreatedAt   time.Time
}

func (stackRow) TableName() string { return "stacks" }

type volRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	CreatedAt   time.Time
}

func (volRow) TableName() string { return "volumes" }

type userRow struct {
	ID                  uint `gorm:"primaryKey"`
	Email               string
	Active              bool
	ScheduledDeletionAt *time.Time
}

func (userRow) TableName() string { return "users" }

// In production these ops drive Docker; here they record calls and mutate the DB so the cascade's effect is
// observable. fakeDBOps keeps the real service's "refuse while a logical DB is attached" guard, proving
// DeleteOwned detaches logical databases before deleting the instance.

type fakeAppOps struct {
	db               *gorm.DB
	stopped, deleted []uint
}

func (f *fakeAppOps) Stop(_ context.Context, app *models.Application) error {
	f.stopped = append(f.stopped, app.ID)
	return nil
}
func (f *fakeAppOps) Delete(_ context.Context, app *models.Application) error {
	f.deleted = append(f.deleted, app.ID)
	return f.db.Delete(&models.Application{}, app.ID).Error
}

type fakeDBOps struct {
	db                         *gorm.DB
	stopped, deleted, detached []uint
}

func (f *fakeDBOps) Stop(_ context.Context, inst *models.DatabaseInstance) error {
	f.stopped = append(f.stopped, inst.ID)
	return nil
}
func (f *fakeDBOps) Delete(_ context.Context, inst *models.DatabaseInstance) error {
	var attached int64
	f.db.Model(&ldbRow{}).Where("instance_id = ? AND application_id IS NOT NULL", inst.ID).Count(&attached)
	if attached > 0 {
		return errors.New("instance still backs an application")
	}
	f.deleted = append(f.deleted, inst.ID)
	f.db.Where("instance_id = ?", inst.ID).Delete(&ldbRow{})
	return f.db.Delete(&models.DatabaseInstance{}, inst.ID).Error
}
func (f *fakeDBOps) DetachFromApp(_ uint, dbID uint) (*models.Database, error) {
	f.detached = append(f.detached, dbID)
	return nil, f.db.Model(&ldbRow{}).Where("id = ?", dbID).Update("application_id", nil).Error
}

type fakeStackOps struct {
	db      *gorm.DB
	deleted []uint
}

func (f *fakeStackOps) Delete(_ context.Context, _ uint, id uint, _ bool) error {
	f.deleted = append(f.deleted, id)
	return f.db.Delete(&models.Stack{}, id).Error
}

type fakeStorageOps struct {
	db      *gorm.DB
	deleted []uint
}

func (f *fakeStorageOps) List(workspaceID uint) ([]models.Volume, error) {
	var vols []models.Volume
	return vols, f.db.Where("workspace_id = ?", workspaceID).Find(&vols).Error
}
func (f *fakeStorageOps) Delete(_ context.Context, v *models.Volume) error {
	f.deleted = append(f.deleted, v.ID)
	return f.db.Delete(&models.Volume{}, v.ID).Error
}

// --- harness -----------------------------------------------------------------

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Shared-cache named memory DB so the pool sees one schema.
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&userRow{}, &wsRow{}, &memberRow{}, &inviteRow{}, &appRow{}, &instRow{}, &ldbRow{}, &stackRow{}, &volRow{}); err != nil {
		t.Fatal(err)
	}
	// WorkspaceRepository.Delete cascades every workspace-owned table; create the ones this minimal schema
	// doesn't already have (empty) so the cascade's scoped deletes run instead of erroring on a missing table.
	// Keep in sync with that cascade's table list. table -> the column it is scoped by.
	cascadeTables := map[string]string{
		"application_networks": "application_id", "database_instance_networks": "database_instance_id",
		"app_env_vars": "application_id", "app_ports": "application_id", "deployments": "application_id",
		"releases": "application_id", "app_events": "application_id", "metric_samples": "application_id",
		"port_bindings": "workspace_id", "jobs": "workspace_id", "backups": "database_id",
		"backup_schedules": "workspace_id", "volume_backups": "volume_id", "dns_records": "domain_id",
		"pipeline_step_runs": "pipeline_run_id", "pipeline_runs": "workspace_id", "images": "workspace_id",
		"stack_env_vars": "stack_id", "webhook_deliveries": "workspace_id", "release_approvals": "workspace_id",
		"environments":      "workspace_id",
		"template_installs": "workspace_id", "routes": "workspace_id", "certificates": "workspace_id",
		"domains": "workspace_id", "dns_providers": "workspace_id", "registries": "workspace_id",
		"git_repositories": "workspace_id", "git_sources": "workspace_id", "networks": "workspace_id",
		"secrets": "workspace_id", "middlewares": "workspace_id", "webhooks": "workspace_id",
		"notification_channels": "workspace_id", "pipeline_definitions": "workspace_id",
		// Templates hang off a (workspace-owned) source, not the workspace directly.
		"template_sources": "workspace_id", "templates": "source_id",
		"workspace_keys": "workspace_id", "workspace_backup_settings": "workspace_id",
	}
	for tbl, col := range cascadeTables {
		if err := db.Exec(fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (id INTEGER PRIMARY KEY, %s INTEGER)", tbl, col)).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func newService(db *gorm.DB) (*Service, *fakeAppOps, *fakeDBOps, *fakeStackOps, *fakeStorageOps) {
	ao := &fakeAppOps{db: db}
	do := &fakeDBOps{db: db}
	so := &fakeStackOps{db: db}
	vo := &fakeStorageOps{db: db}
	svc := NewService(
		repositories.NewUserRepository(db), repositories.NewWorkspaceRepository(db),
		repositories.NewApplicationRepository(db), repositories.NewDatabaseRepository(db),
		repositories.NewStackRepository(db),
		ao, do, so, vo,
	)
	return svc, ao, do, so, vo
}

func mk[T any](t *testing.T, db *gorm.DB, row *T) *T {
	t.Helper()
	if err := db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

// --- tests -------------------------------------------------------------------

func TestStopOwned(t *testing.T) {
	db := newTestDB(t)
	svc, ao, do, _, _ := newService(db)

	ws1 := mk(t, db, &wsRow{OwnerID: 1})
	running := mk(t, db, &appRow{WorkspaceID: ws1.ID, Status: string(models.AppStatusRunning)})
	mk(t, db, &appRow{WorkspaceID: ws1.ID, Status: string(models.AppStatusStopped)})
	runDB := mk(t, db, &instRow{WorkspaceID: ws1.ID, Status: string(models.DBStatusRunning)})
	mk(t, db, &instRow{WorkspaceID: ws1.ID, Status: string(models.DBStatusStopped)})
	// Another user's running app must not be touched.
	ws2 := mk(t, db, &wsRow{OwnerID: 2})
	otherApp := mk(t, db, &appRow{WorkspaceID: ws2.ID, Status: string(models.AppStatusRunning)})

	res := svc.StopOwned(context.Background(), 1)

	if res.Apps != 1 || res.Databases != 1 {
		t.Fatalf("StopOwned = %+v, want apps=1 databases=1", res)
	}
	if len(ao.stopped) != 1 || ao.stopped[0] != running.ID {
		t.Errorf("apps stopped = %v, want [%d]", ao.stopped, running.ID)
	}
	if len(do.stopped) != 1 || do.stopped[0] != runDB.ID {
		t.Errorf("dbs stopped = %v, want [%d]", do.stopped, runDB.ID)
	}
	for _, id := range ao.stopped {
		if id == otherApp.ID {
			t.Error("stopped another user's app — ownership isolation broken")
		}
	}
}

func TestDeleteOwned(t *testing.T) {
	db := newTestDB(t)
	svc, _, do, _, _ := newService(db)

	ws1 := mk(t, db, &wsRow{OwnerID: 1})
	app1 := mk(t, db, &appRow{WorkspaceID: ws1.ID, Status: string(models.AppStatusRunning)})
	inst1 := mk(t, db, &instRow{WorkspaceID: ws1.ID, Status: string(models.DBStatusRunning)})
	logical := mk(t, db, &ldbRow{WorkspaceID: ws1.ID, InstanceID: inst1.ID, ApplicationID: &app1.ID})
	mk(t, db, &stackRow{WorkspaceID: ws1.ID})
	mk(t, db, &volRow{WorkspaceID: ws1.ID})

	ws3 := mk(t, db, &wsRow{OwnerID: 1})
	mk(t, db, &appRow{WorkspaceID: ws3.ID, Status: string(models.AppStatusStopped)})
	mk(t, db, &instRow{WorkspaceID: ws3.ID, Status: string(models.DBStatusStopped)})

	// User 2's workspace must remain intact.
	ws2 := mk(t, db, &wsRow{OwnerID: 2})
	otherApp := mk(t, db, &appRow{WorkspaceID: ws2.ID, Status: string(models.AppStatusRunning)})
	otherVol := mk(t, db, &volRow{WorkspaceID: ws2.ID})

	res := svc.DeleteOwned(context.Background(), 1)

	if res.Workspaces != 2 || res.Apps != 2 || res.Databases != 2 || res.Stacks != 1 || res.Volumes != 1 {
		t.Fatalf("DeleteOwned = %+v, want ws=2 apps=2 db=2 stacks=1 volumes=1", res)
	}
	// The attached logical DB must have been detached before its instance was
	// deleted (the fake refuses to delete an in-use instance, so a missing detach
	// would have dropped Databases below 2 and failed the assertion above).
	if len(do.detached) != 1 || do.detached[0] != logical.ID {
		t.Errorf("detached = %v, want [%d]", do.detached, logical.ID)
	}

	// All of user 1's data is gone.
	assertCount(t, db, &wsRow{}, "owner_id = ?", 0, uint(1))
	assertCount(t, db, &appRow{}, "workspace_id IN (?, ?)", 0, ws1.ID, ws3.ID)
	assertCount(t, db, &instRow{}, "workspace_id IN (?, ?)", 0, ws1.ID, ws3.ID)
	assertCount(t, db, &ldbRow{}, "instance_id = ?", 0, inst1.ID)
	assertCount(t, db, &stackRow{}, "workspace_id = ?", 0, ws1.ID)
	assertCount(t, db, &volRow{}, "workspace_id = ?", 0, ws1.ID)

	// User 2's data is untouched.
	assertCount(t, db, &wsRow{}, "id = ?", 1, ws2.ID)
	assertCount(t, db, &appRow{}, "id = ?", 1, otherApp.ID)
	assertCount(t, db, &volRow{}, "id = ?", 1, otherVol.ID)
}

func TestPurgeDue(t *testing.T) {
	db := newTestDB(t)
	svc, _, _, _, _ := newService(db)

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(72 * time.Hour)

	// Due: grace elapsed → purged with its data.
	due := mk(t, db, &userRow{Email: "due@x", ScheduledDeletionAt: &past})
	wsDue := mk(t, db, &wsRow{OwnerID: due.ID})
	mk(t, db, &appRow{WorkspaceID: wsDue.ID, Status: string(models.AppStatusStopped)})
	mk(t, db, &volRow{WorkspaceID: wsDue.ID})

	// Scheduled but not yet due, and a normal account → both untouched.
	notYet := mk(t, db, &userRow{Email: "soon@x", ScheduledDeletionAt: &future})
	wsNotYet := mk(t, db, &wsRow{OwnerID: notYet.ID})
	mk(t, db, &appRow{WorkspaceID: wsNotYet.ID, Status: string(models.AppStatusRunning)})
	normal := mk(t, db, &userRow{Email: "ok@x"})
	wsNormal := mk(t, db, &wsRow{OwnerID: normal.ID})

	if n := svc.PurgeDue(context.Background()); n != 1 {
		t.Fatalf("PurgeDue purged %d, want 1", n)
	}

	// The due account and all its data are gone.
	assertCount(t, db, &userRow{}, "id = ?", 0, due.ID)
	assertCount(t, db, &wsRow{}, "id = ?", 0, wsDue.ID)
	assertCount(t, db, &appRow{}, "workspace_id = ?", 0, wsDue.ID)
	assertCount(t, db, &volRow{}, "workspace_id = ?", 0, wsDue.ID)

	// The not-yet-due and normal accounts (and their workspaces) remain.
	assertCount(t, db, &userRow{}, "id IN (?, ?)", 2, notYet.ID, normal.ID)
	assertCount(t, db, &wsRow{}, "id IN (?, ?)", 2, wsNotYet.ID, wsNormal.ID)
	assertCount(t, db, &appRow{}, "workspace_id = ?", 1, wsNotYet.ID)
}

func assertCount(t *testing.T, db *gorm.DB, model any, where string, want int64, args ...any) {
	t.Helper()
	var n int64
	db.Model(model).Where(where, args...).Count(&n)
	if n != want {
		t.Errorf("%T count where %q = %d, want %d", model, where, n, want)
	}
}
