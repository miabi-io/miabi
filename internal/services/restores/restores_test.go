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

package restores

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeReporter struct {
	mu    sync.Mutex
	got   []Report
	users []uint
	done  chan struct{}
}

func (f *fakeReporter) RequestedRestoreFinished(_, user uint, rep Report) {
	f.mu.Lock()
	f.got, f.users = append(f.got, rep), append(f.users, user)
	f.mu.Unlock()
	f.done <- struct{}{}
}

func newRunner(t *testing.T) (*Runner, *fakeReporter, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Restore{}); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(repositories.NewRestoreRepository(db))
	rep := &fakeReporter{done: make(chan struct{}, 4)}
	r.SetReporter(rep)
	return r, rep, db
}

func TestStartRunsInTheBackgroundAndReports(t *testing.T) {
	r, rep, db := newRunner(t)
	user := uint(7)
	release := make(chan struct{})
	rec, err := r.Start(&models.Restore{WorkspaceID: 1, Kind: models.RestoreDatabase, InstanceID: 3, DatabaseID: 9, Source: "backup #5", RequestedByID: &user},
		"shop", "/databases/3", func(context.Context) (Outcome, error) {
			<-release
			return Outcome{}, nil
		})
	if err != nil || rec.Status != models.BackupRunning || rec.ID == 0 {
		t.Fatalf("Start = %+v, %v; want a running row while the restore continues", rec, err)
	}

	if _, err := r.Start(&models.Restore{WorkspaceID: 1, Kind: models.RestoreDatabase, InstanceID: 3, DatabaseID: 9}, "shop", "", nil); !errors.Is(err, ErrRunning) {
		t.Errorf("second restore of the same database err = %v, want ErrRunning", err)
	}
	if _, err := r.Start(&models.Restore{WorkspaceID: 1, Kind: models.RestoreRecoveryPoint, InstanceID: 3}, "pg", "", nil); !errors.Is(err, ErrRunning) {
		t.Errorf("recovery point restore of the busy instance err = %v, want ErrRunning", err)
	}

	close(release)
	<-rep.done
	var got models.Restore
	db.First(&got, rec.ID)
	if got.Status != models.BackupCompleted || got.FinishedAt == nil {
		t.Errorf("finished row = %s, finished %v; want completed", got.Status, got.FinishedAt)
	}
	if len(rep.users) != 1 || rep.users[0] != 7 || !rep.got[0].OK || rep.got[0].Subject != "shop" {
		t.Errorf("report = %+v to %v, want one OK report to the requester", rep.got, rep.users)
	}
}

// A recovery point restore keeps going past a failed database; the row and the report say which.
func TestPartialRecoveryPointRestoreIsFailedWithItsLists(t *testing.T) {
	r, rep, db := newRunner(t)
	user := uint(7)
	rec, err := r.Start(&models.Restore{WorkspaceID: 1, Kind: models.RestoreRecoveryPoint, InstanceID: 4, Source: "mbdb_pg_x", RequestedByID: &user},
		"pg", "/databases/4", func(context.Context) (Outcome, error) {
			return Outcome{Restored: []string{"orders"}, Failed: []string{"users: exit 1"}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	<-rep.done
	var got models.Restore
	db.First(&got, rec.ID)
	if got.Status != models.BackupFailed || len(got.Restored) != 1 || len(got.Failed) != 1 || got.Error == "" {
		t.Errorf("row = %s restored %v failed %v err %q", got.Status, got.Restored, got.Failed, got.Error)
	}
	if rep.got[0].OK || len(rep.got[0].Restored) != 1 {
		t.Errorf("report = %+v, want a partial failure", rep.got[0])
	}
}

func TestAPanickingRestoreIsRecordedAsFailed(t *testing.T) {
	r, rep, db := newRunner(t)
	user := uint(7)
	rec, err := r.Start(&models.Restore{WorkspaceID: 1, Kind: models.RestoreVolume, VolumeID: 2, RequestedByID: &user},
		"data", "", func(context.Context) (Outcome, error) { panic("nil map") })
	if err != nil {
		t.Fatal(err)
	}
	<-rep.done
	var got models.Restore
	db.First(&got, rec.ID)
	if got.Status != models.BackupFailed || got.Error == "" {
		t.Errorf("row = %s %q, want failed with the panic", got.Status, got.Error)
	}
}

func TestAbandonedRestoresAreClosedAndStopBlocking(t *testing.T) {
	r, _, db := newRunner(t)
	old := time.Now().Add(-time.Hour)
	stuck := models.Restore{WorkspaceID: 1, Kind: models.RestoreVolume, VolumeID: 5, Status: models.BackupRunning, StartedAt: &old, HeartbeatAt: &old}
	db.Create(&stuck)
	if live, _ := r.Live(models.Restore{VolumeID: 5}); live != nil {
		t.Error("an abandoned restore still blocks its volume")
	}
	if err := r.FailStale(); err != nil {
		t.Fatal(err)
	}
	var got models.Restore
	db.First(&got, stuck.ID)
	if got.Status != models.BackupFailed || got.Error == "" {
		t.Errorf("abandoned restore = %s %q, want failed", got.Status, got.Error)
	}
}
