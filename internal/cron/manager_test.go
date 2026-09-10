// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cron

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The backup service is built once for the HTTP process and again for the worker,
// and only one of them had the set repository wired — so manual recovery points
// worked and scheduled ones failed with "backup sets are not available".
// NewManager now wires it, and this pins that.
func TestNewManagerGivesTheBackupServiceItsSetRepository(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.DatabaseBackupSet{}, &models.Backup{}); err != nil {
		t.Fatal(err)
	}
	sets := repositories.NewDatabaseBackupSetRepository(db)
	// Deliberately NOT calling SetSetRepository here: that is the caller mistake
	// this guards against.
	svc := backup.NewService(repositories.NewBackupRepository(db), repositories.NewDatabaseRepository(db), nil)

	NewManager(svc, repositories.NewDatabaseRepository(db), repositories.NewBackupRepository(db), sets, nil)

	if _, err := svc.ListSets(1); errors.Is(err, backup.ErrSetsUnavailable) {
		t.Fatal("the scheduler's backup service cannot reach the set repository")
	}

	// RunSet must get past the availability check too — it fails later, on the
	// missing S3 target, which is the next gate rather than this one.
	inst := &models.DatabaseInstance{ID: 7, Name: "pg", WorkspaceID: 1, Engine: models.DBEnginePostgres}
	_, err = svc.RunSet(context.Background(), inst, backup.SetOptions{Trigger: "scheduled"}, backup.Destination{Type: "local"})
	if errors.Is(err, backup.ErrSetsUnavailable) {
		t.Fatal("a scheduled recovery point still reports the set repository unavailable")
	}
	if !errors.Is(err, backup.ErrS3Required) {
		t.Fatalf("error = %v, want it to reach the S3 check", err)
	}
}

// A manager built without a set repository must not panic; sets are simply
// unavailable, as they are on a build that never wired them.
func TestNewManagerToleratesNoSetRepository(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	svc := backup.NewService(repositories.NewBackupRepository(db), repositories.NewDatabaseRepository(db), nil)
	m := NewManager(svc, repositories.NewDatabaseRepository(db), repositories.NewBackupRepository(db), nil, nil)
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if _, err := svc.ListSets(1); !errors.Is(err, backup.ErrSetsUnavailable) {
		t.Errorf("error = %v, want ErrSetsUnavailable", err)
	}
}
