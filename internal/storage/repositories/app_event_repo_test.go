// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newEventDB(t *testing.T) *AppEventRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.AppEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewAppEventRepository(db)
}

func appEvent(appID uint) *models.AppEvent {
	return &models.AppEvent{WorkspaceID: 1, SubjectType: models.SubjectApp, ApplicationID: appID, Type: models.EventDeploySucceeded}
}

func dbEvent(dbID uint) *models.AppEvent {
	return &models.AppEvent{WorkspaceID: 1, SubjectType: models.SubjectDatabase, DatabaseID: dbID, Type: models.EventDatabaseStarted}
}

// TestEventSubjectIsolation is the load-bearing test for putting both subjects in one table:
// an app's timeline must never show database events and vice versa. Both kinds carry 0 in the
// other subject's id column, so a query that forgot its subject filter would match everything.
func TestEventSubjectIsolation(t *testing.T) {
	repo := newEventDB(t)
	for _, e := range []*models.AppEvent{appEvent(7), dbEvent(7), appEvent(7), dbEvent(9), dbEvent(7)} {
		if err := repo.Create(e); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	// App 7 and database 7 share an id — only the subject column separates them.
	appEvents, err := repo.ListByApp(7, 0, 0)
	if err != nil {
		t.Fatalf("ListByApp: %v", err)
	}
	if len(appEvents) != 2 {
		t.Errorf("ListByApp(7) = %d events, want 2 (database events leaked in)", len(appEvents))
	}
	for _, e := range appEvents {
		if e.DatabaseID != 0 {
			t.Errorf("ListByApp returned a database event (db %d)", e.DatabaseID)
		}
	}

	dbEvents, err := repo.ListByDatabase(7, 0, 0)
	if err != nil {
		t.Fatalf("ListByDatabase: %v", err)
	}
	if len(dbEvents) != 2 {
		t.Errorf("ListByDatabase(7) = %d events, want 2", len(dbEvents))
	}
	for _, e := range dbEvents {
		if e.ApplicationID != 0 {
			t.Errorf("ListByDatabase returned an app event (app %d)", e.ApplicationID)
		}
	}
}

// TestDeleteByDatabaseLeavesOtherSubjects pins that a delete takes only that instance's events,
// not the same-numbered application's nor another instance's.
func TestDeleteByDatabaseLeavesOtherSubjects(t *testing.T) {
	repo := newEventDB(t)
	for _, e := range []*models.AppEvent{appEvent(7), dbEvent(7), dbEvent(9)} {
		if err := repo.Create(e); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if err := repo.DeleteByDatabase(7); err != nil {
		t.Fatalf("DeleteByDatabase: %v", err)
	}
	if got, _ := repo.ListByDatabase(7, 0, 0); len(got) != 0 {
		t.Errorf("database 7 still has %d events", len(got))
	}
	if got, _ := repo.ListByApp(7, 0, 0); len(got) != 1 {
		t.Errorf("app 7 lost events to the database delete: %d, want 1", len(got))
	}
	if got, _ := repo.ListByDatabase(9, 0, 0); len(got) != 1 {
		t.Errorf("database 9 lost events to the database 7 delete: %d, want 1", len(got))
	}
}

func TestTrimByDatabase(t *testing.T) {
	repo := newEventDB(t)
	if err := repo.Create(appEvent(3)); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := repo.Create(dbEvent(3)); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if err := repo.TrimByDatabase(3, 2); err != nil {
		t.Fatalf("TrimByDatabase: %v", err)
	}
	kept, _ := repo.ListByDatabase(3, 0, 0)
	if len(kept) != 2 {
		t.Errorf("kept %d events, want 2", len(kept))
	}
	if got, _ := repo.ListByApp(3, 0, 0); len(got) != 1 {
		t.Errorf("trimming database 3 also trimmed app 3: %d events left, want 1", len(got))
	}
}
