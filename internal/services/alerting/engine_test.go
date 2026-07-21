// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package alerting

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeMembers struct{ m []models.WorkspaceMember }

func (f fakeMembers) ListMembers(uint) ([]models.WorkspaceMember, error) { return f.m, nil }

type fakeNamer struct{}

func (fakeNamer) AppName(uint) string { return "web" }

type fakeBus struct{ pushes map[string]int }

func (b *fakeBus) Publish(topic string, _ eventbus.Event) { b.pushes[topic]++ }

func newEngineDB(t *testing.T) (*Engine, *repositories.AlertRepository, *repositories.NotificationInboxRepository, *fakeBus) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Alert{}, &models.Notification{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The active-alert uniqueness (mirrors the Postgres partial index).
	if err := db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_alert_active_dedup ON alerts (workspace_id, dedup_key) WHERE state IN ('firing','acknowledged')`).Error; err != nil {
		t.Fatalf("index: %v", err)
	}
	alerts := repositories.NewAlertRepository(db)
	inbox := repositories.NewNotificationInboxRepository(db)
	members := fakeMembers{m: []models.WorkspaceMember{
		{UserID: 7, Role: models.WorkspaceRoleDeveloper},
		{UserID: 9, Role: models.WorkspaceRoleViewer}, // below MinRole (developer) → excluded
	}}
	bus := &fakeBus{pushes: map[string]int{}}
	eng := NewEngine(alerts, inbox, members, fakeNamer{}, bus, NewMemoryCounter(nil))
	return eng, alerts, inbox, bus
}

func die(ws, app uint) *models.AppEvent {
	return &models.AppEvent{WorkspaceID: ws, ApplicationID: app, Type: models.EventContainerDied, Severity: models.SeverityError, Message: "exited 137"}
}

// TestCrashLoopThesis is acceptance #1: 40 die events collapse to ONE alert whose
// count climbs and ONE unread notification (not 40); a healthy signal auto-resolves
// it to ONE "recovered" notification.
func TestCrashLoopThesis(t *testing.T) {
	eng, alerts, inbox, bus := newEngineDB(t)

	for i := 0; i < 40; i++ {
		eng.OnEvent(die(1, 42))
	}

	// Exactly one active alert for the crash-loop condition.
	active, err := alerts.ListByWorkspace(1, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("want 1 active alert, got %d", len(active))
	}
	a := active[0]
	if a.DedupKey != "crashloop:app:42" || a.Severity != models.AlertCritical {
		t.Fatalf("wrong alert: %+v", a)
	}
	// Threshold 5 → first fire at the 5th die, folds 6..40 → count 36.
	if a.Count != 36 {
		t.Fatalf("alert count = %d, want 36", a.Count)
	}

	// The eligible member (developer) has exactly ONE unread notification; the
	// viewer (below MinRole) has none.
	ns, err := inbox.ListByUser(7, 0, false, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 || ns[0].ReadAt != nil {
		t.Fatalf("developer inbox: want 1 unread, got %d (%+v)", len(ns), ns)
	}
	if vn, _ := inbox.ListByUser(9, 0, false, 0, 100); len(vn) != 0 {
		t.Fatalf("viewer should get nothing, got %d", len(vn))
	}
	if c, _ := inbox.UnreadCount(7); c != 1 {
		t.Fatalf("unread count = %d, want 1", c)
	}

	// Auto-resolve: a healthy signal closes the alert and rewrites the single
	// notification to "recovered" (still one row), re-surfaced as unread.
	eng.OnEvent(&models.AppEvent{WorkspaceID: 1, ApplicationID: 42, Type: models.EventContainerHealth, Severity: models.SeverityInfo, Message: "healthy"})

	if act, _ := alerts.ListByWorkspace(1, true, 0); len(act) != 0 {
		t.Fatalf("alert should be resolved, %d still active", len(act))
	}
	ns, _ = inbox.ListByUser(7, 0, false, 0, 100)
	if len(ns) != 1 {
		t.Fatalf("still want exactly 1 notification after resolve, got %d", len(ns))
	}
	if ns[0].ReadAt != nil {
		t.Fatalf("recovered notification should be unread (re-surfaced)")
	}
	if ns[0].Title != "Recovered — web" {
		t.Fatalf("recovered title = %q", ns[0].Title)
	}
	// SSE pushed the developer's topic (fire + resolve), never the viewer's.
	if bus.pushes[NotificationTopic(7)] == 0 {
		t.Fatal("expected SSE pushes to the developer")
	}
	if bus.pushes[NotificationTopic(9)] != 0 {
		t.Fatal("viewer must never be pushed")
	}
}

// TestReFireNotSpam is acceptance #2: unhealthy → healthy → unhealthy re-fires and
// resolves without a running tally of duplicates.
func TestReFireNotSpam(t *testing.T) {
	eng, alerts, inbox, _ := newEngineDB(t)
	unhealthy := &models.AppEvent{WorkspaceID: 1, ApplicationID: 5, Type: models.EventContainerHealth, Severity: models.SeverityWarning}
	healthy := &models.AppEvent{WorkspaceID: 1, ApplicationID: 5, Type: models.EventContainerHealth, Severity: models.SeverityInfo}

	eng.OnEvent(unhealthy)
	eng.OnEvent(healthy)
	eng.OnEvent(unhealthy)

	// One active alert again (the re-fire created a fresh one after resolve).
	act, _ := alerts.ListByWorkspace(1, true, 0)
	if len(act) != 1 || act[0].DedupKey != "unhealthy:app:5" {
		t.Fatalf("want 1 active unhealthy alert, got %+v", act)
	}
	// The developer has a single notification row throughout (upsert per user+alert
	// on the first, a new row for the re-fired alert = 2 total; never a tally).
	ns, _ := inbox.ListByUser(7, 0, false, 0, 100)
	if len(ns) == 0 || len(ns) > 2 {
		t.Fatalf("want 1-2 notifications, got %d", len(ns))
	}
}
