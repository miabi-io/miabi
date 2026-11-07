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

package logbackfill

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Application's UIDModel uuid default breaks sqlite migration, so the table is created by hand.
	if err := db.Exec(`CREATE TABLE applications (id integer primary key, workspace_id integer)`).Error; err != nil {
		t.Fatalf("create applications: %v", err)
	}
	if err := db.AutoMigrate(
		&models.UpgradeStep{}, &models.Deployment{}, &models.PipelineRun{},
		&models.PipelineStepRun{}, &models.Job{}, &models.Backup{},
		&models.VolumeBackup{}, &models.PlatformBackup{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newStore(t *testing.T) *logstore.Store {
	t.Helper()
	be, err := logstore.NewFSBackend(t.TempDir())
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	return logstore.New(be, logstore.Config{Compress: true, TailBytes: 8})
}

func bigLog() string { return strings.Repeat("line of build output\n", 50) }

func TestBackfillExternalizesLargeRows(t *testing.T) {
	db := newDB(t)
	store := newStore(t)

	db.Exec(`INSERT INTO applications (id, workspace_id) VALUES (1, 42)`)
	db.Create(&models.Deployment{ApplicationID: 1, Number: 1, Logs: bigLog()})
	db.Create(&models.Deployment{ApplicationID: 1, Number: 2, Logs: "tiny\n"})

	// A pipeline step under run 9 (workspace 42).
	db.Create(&models.PipelineRun{WorkspaceID: 42})
	db.Create(&models.PipelineStepRun{PipelineRunID: 1, Ordinal: 2, Logs: bigLog()})

	// Job + backups carrying their own workspace id; platform backup has none.
	db.Create(&models.Job{WorkspaceID: 42, ApplicationID: 1, Logs: bigLog()})
	db.Create(&models.Backup{WorkspaceID: 42, DatabaseID: 1, Logs: bigLog()})
	db.Create(&models.VolumeBackup{WorkspaceID: 42, VolumeID: 1, Logs: bigLog()})
	db.Create(&models.PlatformBackup{Subject: models.PlatformBackupDatabase, Logs: bigLog()})

	if err := Run(context.Background(), db, store, 8, "test"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Every large row now has a ref, a shrunken tail, and its full log in the store.
	assertExternalized(t, db, store, "deployments", 1, logstore.DeploymentRef(42, 1, 1))
	assertExternalized(t, db, store, "pipeline_step_runs", 1, logstore.PipelineStepRef(42, 1, 2))
	assertExternalized(t, db, store, "jobs", 1, logstore.JobRef(42, 1))
	assertExternalized(t, db, store, "backups", 1, logstore.BackupRef(42, 1))
	assertExternalized(t, db, store, "volume_backups", 1, logstore.VolumeBackupRef(42, 1))
	assertExternalized(t, db, store, "platform_backups", 1, logstore.PlatformBackupRef(1))

	// The small deployment stays inline (no ref, full log intact).
	var small models.Deployment
	db.First(&small, 2)
	if small.LogRef != "" || small.Logs != "tiny\n" {
		t.Errorf("small row externalized unexpectedly: ref=%q logs=%q", small.LogRef, small.Logs)
	}

	// The marker is recorded exactly once.
	var markers int64
	db.Model(&models.UpgradeStep{}).Where("name = ?", stepName).Count(&markers)
	if markers != 1 {
		t.Errorf("markers = %d, want 1", markers)
	}
}

func TestBackfillIdempotent(t *testing.T) {
	db := newDB(t)
	store := newStore(t)
	db.Exec(`INSERT INTO applications (id, workspace_id) VALUES (1, 42)`)
	db.Create(&models.Job{WorkspaceID: 42, ApplicationID: 1, Logs: bigLog()})

	if err := Run(context.Background(), db, store, 8, "test"); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	var afterFirst models.Job
	db.First(&afterFirst, 1)

	// A second run is a no-op (marker present): the row is unchanged.
	if err := Run(context.Background(), db, store, 8, "test"); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	var afterSecond models.Job
	db.First(&afterSecond, 1)
	if afterSecond.LogRef != afterFirst.LogRef || afterSecond.Logs != afterFirst.Logs {
		t.Error("second run mutated an already-externalized row")
	}
	var markers int64
	db.Model(&models.UpgradeStep{}).Where("name = ?", stepName).Count(&markers)
	if markers != 1 {
		t.Errorf("markers = %d, want 1 after two runs", markers)
	}
}

func TestBackfillDisabledStoreIsNoOp(t *testing.T) {
	db := newDB(t)
	db.Create(&models.Job{WorkspaceID: 42, ApplicationID: 1, Logs: bigLog()})

	// Disabled must record no marker, so enabling the store on a later boot still runs the backfill.
	if err := Run(context.Background(), db, nil, 8, "test"); err != nil {
		t.Fatalf("Run disabled: %v", err)
	}
	var j models.Job
	db.First(&j, 1)
	if j.LogRef != "" {
		t.Errorf("disabled store externalized a row: ref=%q", j.LogRef)
	}
	var markers int64
	db.Model(&models.UpgradeStep{}).Where("name = ?", stepName).Count(&markers)
	if markers != 0 {
		t.Errorf("markers = %d, want 0 for disabled store", markers)
	}
}

func assertExternalized(t *testing.T, db *gorm.DB, store *logstore.Store, table string, id uint, wantRef string) {
	t.Helper()
	var row struct {
		Logs   string
		LogRef string
	}
	if err := db.Table(table).Select("logs, log_ref").Where("id = ?", id).Scan(&row).Error; err != nil {
		t.Fatalf("%s/%d: scan: %v", table, id, err)
	}
	if row.LogRef != wantRef {
		t.Errorf("%s/%d: ref = %q, want %q", table, id, row.LogRef, wantRef)
	}
	if len(row.Logs) >= len(bigLog()) {
		t.Errorf("%s/%d: tail not trimmed (%d bytes)", table, id, len(row.Logs))
	}
	rc, err := store.Open(wantRef)
	if err != nil {
		t.Fatalf("%s/%d: open store object: %v", table, id, err)
	}
	defer func() { _ = rc.Close() }()
	buf := new(strings.Builder)
	if _, err := io.Copy(buf, rc); err != nil {
		t.Fatalf("%s/%d: read store object: %v", table, id, err)
	}
	if buf.String() != bigLog() {
		t.Errorf("%s/%d: stored object != original full log", table, id)
	}
}
