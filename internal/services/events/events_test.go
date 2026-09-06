// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newService(t *testing.T) (*Service, *eventbus.Bus) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.AppEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	bus := eventbus.New()
	return NewService(repositories.NewAppEventRepository(db), bus), bus
}

// TestRecordRoutesBySubject pins that a database event reaches the database topic, which the
// instance detail page subscribes to, and not the application topic.
func TestRecordRoutesBySubject(t *testing.T) {
	svc, bus := newService(t)
	dbCh, stopDB := bus.Subscribe(DatabaseTopic(4))
	defer stopDB()
	appCh, stopApp := bus.Subscribe(Topic(4))
	defer stopApp()

	svc.EmitDatabase(1, 4, "orders-db", models.EventDatabaseStarted, models.SeverityInfo, "Instance started", nil, nil)

	select {
	case e := <-dbCh:
		got, ok := e.Data.(*models.AppEvent)
		if !ok || got.DatabaseID != 4 || got.SubjectType != models.SubjectDatabase {
			t.Fatalf("database topic got %+v", e.Data)
		}
		// The name rides the live event so SSE consumers can label it without a lookup.
		if got.DatabaseName != "orders-db" {
			t.Errorf("DatabaseName = %q, want orders-db", got.DatabaseName)
		}
	default:
		t.Fatal("database topic received nothing")
	}
	select {
	case e := <-appCh:
		t.Fatalf("app topic received a database event: %+v", e.Data)
	default:
	}
}

// TestRecordDropsSubjectlessEvents pins that an event naming no resource is discarded rather
// than persisted as a row no page can reach.
func TestRecordDropsSubjectlessEvents(t *testing.T) {
	svc, _ := newService(t)
	svc.Record(&models.AppEvent{WorkspaceID: 1, Type: models.EventDeploySucceeded})

	got, err := svc.List(0, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("recorded %d subject-less events, want 0", len(got))
	}
}

// TestRecordDefaultsToAppSubject pins that a producer setting only ApplicationID still records
// and streams as an application event.
func TestRecordDefaultsToAppSubject(t *testing.T) {
	svc, bus := newService(t)
	ch, stop := bus.Subscribe(Topic(8))
	defer stop()

	svc.Record(&models.AppEvent{WorkspaceID: 1, ApplicationID: 8, Type: models.EventDeploySucceeded})

	select {
	case e := <-ch:
		got := e.Data.(*models.AppEvent)
		if got.SubjectType != models.SubjectApp {
			t.Errorf("SubjectType = %q, want app", got.SubjectType)
		}
	default:
		t.Fatal("app topic received nothing")
	}
}
