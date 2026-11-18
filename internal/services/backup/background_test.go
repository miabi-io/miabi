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

package backup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func TestDetachReturnsOnceTheRowExists(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	started := make(chan *models.Backup, 1)
	row, err := detach(started, func(ctx context.Context) error {
		started <- &models.Backup{ID: 9, Status: models.BackupRunning}
		<-release
		close(finished)
		return nil
	})
	if err != nil || row == nil || row.ID != 9 {
		t.Fatalf("detach = %+v, %v; want the running row while the run continues", row, err)
	}
	select {
	case <-finished:
		t.Fatal("the run finished before detach returned; it must not wait for it")
	default:
	}
	close(release)
	<-finished
}

func TestDetachReturnsAnEarlyError(t *testing.T) {
	started := make(chan *models.Backup, 1)
	boom := errors.New("node is offline")
	if _, err := detach(started, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the error raised before the row existed", err)
	}
}

func TestDetachSurvivesAPanic(t *testing.T) {
	started := make(chan *models.Backup, 1)
	if _, err := detach(started, func(context.Context) error { panic("nil map") }); err == nil {
		t.Fatal("a panic before the row existed must come back as an error")
	}
}

// A run whose process died stops beating: it no longer blocks the next one, and the sweep closes it.
func TestAbandonedRunsAreClosed(t *testing.T) {
	svc, sets, db := newSetService(t)
	old := time.Now().Add(-time.Hour)
	fresh := time.Now()
	rows := []models.Backup{
		{WorkspaceID: 1, DatabaseID: 1, Status: models.BackupRunning, StartedAt: &old, HeartbeatAt: &old},
		{WorkspaceID: 1, DatabaseID: 2, Status: models.BackupRunning, StartedAt: &old, HeartbeatAt: &fresh},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	since := time.Now().Add(-staleAfter)
	if running, _ := svc.repo.Running(1, since); running {
		t.Error("an abandoned run still blocks a new backup")
	}
	if running, _ := svc.repo.Running(2, since); !running {
		t.Error("a live run (recent heartbeat) must block a second one")
	}
	set := &models.DatabaseBackupSet{WorkspaceID: 1, InstanceID: 7, Ref: "mbdb_pg_old", Status: models.BackupRunning, StartedAt: &old}
	if err := sets.Create(set); err != nil {
		t.Fatal(err)
	}

	if err := svc.FailStale(); err != nil {
		t.Fatal(err)
	}
	var got []models.Backup
	db.Order("database_id").Find(&got)
	if got[0].Status != models.BackupFailed || got[0].Error == "" || got[1].Status != models.BackupRunning {
		t.Errorf("after the sweep: %s (%q), %s; want the abandoned one failed and the live one running", got[0].Status, got[0].Error, got[1].Status)
	}
	if s, _ := sets.FindByID(set.ID); s.Status != models.BackupFailed {
		t.Errorf("abandoned recovery point is %s, want failed", s.Status)
	}
}

// A mid-run save must not blank the heartbeat, or a slow run would look abandoned.
func TestUpdateKeepsTheHeartbeat(t *testing.T) {
	svc, _, db := newSetService(t)
	now := time.Now()
	b := &models.Backup{WorkspaceID: 1, DatabaseID: 3, Status: models.BackupRunning, StartedAt: &now}
	if err := svc.repo.Create(b); err != nil {
		t.Fatal(err)
	}
	if err := svc.repo.Touch(b.ID); err != nil {
		t.Fatal(err)
	}
	b.SizeBytes = 10
	if err := svc.repo.Update(b); err != nil {
		t.Fatal(err)
	}
	var got models.Backup
	db.First(&got, b.ID)
	if got.HeartbeatAt == nil {
		t.Fatal("Update cleared the heartbeat")
	}
}
