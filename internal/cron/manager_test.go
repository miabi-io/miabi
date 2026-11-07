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

// Both HTTP and worker build the backup service; NewManager must wire the set repository or scheduled points fail.
func TestNewManagerGivesTheBackupServiceItsSetRepository(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.DatabaseBackupSet{}, &models.Backup{}); err != nil {
		t.Fatal(err)
	}
	sets := repositories.NewDatabaseBackupSetRepository(db)
	// Deliberately not calling SetSetRepository: that is the caller mistake this guards against.
	svc := backup.NewService(repositories.NewBackupRepository(db), repositories.NewDatabaseRepository(db), nil)

	NewManager(svc, repositories.NewDatabaseRepository(db), repositories.NewBackupRepository(db), sets, nil)

	if _, err := svc.ListSets(1); errors.Is(err, backup.ErrSetsUnavailable) {
		t.Fatal("the scheduler's backup service cannot reach the set repository")
	}

	// RunSet fails later on the missing S3 target, which is the next gate rather than this one.
	inst := &models.DatabaseInstance{ID: 7, Name: "pg", WorkspaceID: 1, Engine: models.DBEnginePostgres}
	_, err = svc.RunSet(context.Background(), inst, backup.SetOptions{Trigger: "scheduled"}, backup.Destination{Type: "local"})
	if errors.Is(err, backup.ErrSetsUnavailable) {
		t.Fatal("a scheduled recovery point still reports the set repository unavailable")
	}
	if !errors.Is(err, backup.ErrS3Required) {
		t.Fatalf("error = %v, want it to reach the S3 check", err)
	}
}

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
