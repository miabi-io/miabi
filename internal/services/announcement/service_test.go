// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package announcement

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeBus struct{ pushes map[string]int }

func (b *fakeBus) Publish(topic string, _ eventbus.Event) { b.pushes[topic]++ }

func newService(t *testing.T) (*Service, *gorm.DB, *repositories.NotificationInboxRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Announcement{}, &models.Notification{}, &models.User{}, &models.WorkspaceMember{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Mirrors the Postgres partial index that makes delivery idempotent.
	if err := db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_user_announcement ` +
			`ON notifications (user_id, announcement_id) WHERE announcement_id IS NOT NULL`).Error; err != nil {
		t.Fatalf("index: %v", err)
	}
	inbox := repositories.NewNotificationInboxRepository(db)
	svc := NewService(repositories.NewAnnouncementRepository(db), inbox, &fakeBus{pushes: map[string]int{}})
	return svc, db, inbox
}

func seedUsers(t *testing.T, db *gorm.DB) {
	t.Helper()
	users := []models.User{
		{ID: 1, Email: "admin@example.com", Username: "admin", Role: models.SystemRoleAdmin, Active: true},
		{ID: 2, Email: "owner@example.com", Username: "owner", Role: models.SystemRoleUser, Active: true},
		{ID: 3, Email: "dev@example.com", Username: "dev", Role: models.SystemRoleUser, Active: true},
		{ID: 4, Email: "gone@example.com", Username: "gone", Role: models.SystemRoleUser},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	// Active carries a GORM default, so a false at insert time is written as true.
	if err := db.Model(&models.User{}).Where("id = ?", 4).Update("active", false).Error; err != nil {
		t.Fatalf("suspend user: %v", err)
	}
	members := []models.WorkspaceMember{
		{WorkspaceID: 10, UserID: 2, Role: models.WorkspaceRoleOwner},
		{WorkspaceID: 10, UserID: 3, Role: models.WorkspaceRoleDeveloper},
	}
	if err := db.Create(&members).Error; err != nil {
		t.Fatalf("seed members: %v", err)
	}
}

func deliveries(t *testing.T, db *gorm.DB, announcementID uint) []models.Notification {
	t.Helper()
	var out []models.Notification
	if err := db.Where("announcement_id = ?", announcementID).Order("user_id").Find(&out).Error; err != nil {
		t.Fatalf("load deliveries: %v", err)
	}
	return out
}

func TestCreateBroadcastsToActiveUsersOnly(t *testing.T) {
	svc, db, _ := newService(t)
	seedUsers(t, db)

	a := &models.Announcement{Title: "Maintenance", Message: "02:00 UTC", Audience: models.AudienceAll, Severity: models.AlertWarning}
	if err := svc.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	if a.PublishedAt == nil {
		t.Fatal("an unscheduled announcement must publish on creation")
	}
	got := deliveries(t, db, a.ID)
	if len(got) != 3 {
		t.Fatalf("delivered to %d users, want 3 (the suspended account is excluded)", len(got))
	}
	if a.Recipients != 3 {
		t.Fatalf("recipients recorded as %d, want 3", a.Recipients)
	}
	if got[0].WorkspaceID != 0 {
		t.Fatalf("delivery scoped to workspace %d, want 0 (platform-wide)", got[0].WorkspaceID)
	}
	if got[0].Kind != models.NotificationKindAnnouncement {
		t.Fatalf("kind = %q, want %q", got[0].Kind, models.NotificationKindAnnouncement)
	}
}

func TestAudienceNarrowsRecipients(t *testing.T) {
	cases := []struct {
		name     string
		audience models.AnnouncementAudience
		wsIDs    []uint
		want     []uint
	}{
		{"admins", models.AudienceAdmins, nil, []uint{1}},
		{"owners", models.AudienceOwners, nil, []uint{2}},
		{"workspaces", models.AudienceWorkspaces, []uint{10}, []uint{2, 3}},
		{"workspaces with no match", models.AudienceWorkspaces, []uint{99}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, db, _ := newService(t)
			seedUsers(t, db)

			a := &models.Announcement{Title: "Notice", Audience: tc.audience, WorkspaceIDs: tc.wsIDs}
			if err := svc.Create(a); err != nil {
				t.Fatalf("create: %v", err)
			}
			got := deliveries(t, db, a.ID)
			if len(got) != len(tc.want) {
				t.Fatalf("delivered to %d users, want %d", len(got), len(tc.want))
			}
			for i, uid := range tc.want {
				if got[i].UserID != uid {
					t.Fatalf("recipient[%d] = %d, want %d", i, got[i].UserID, uid)
				}
			}
		})
	}
}

func TestScheduledAnnouncementWaitsForTick(t *testing.T) {
	svc, db, _ := newService(t)
	seedUsers(t, db)
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }

	at := now.Add(time.Hour)
	a := &models.Announcement{Title: "Upgrade", Audience: models.AudienceAll, PublishAt: &at}
	if err := svc.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.PublishedAt != nil {
		t.Fatal("a scheduled announcement must not publish on creation")
	}
	if n := len(deliveries(t, db, a.ID)); n != 0 {
		t.Fatalf("%d deliveries before the scheduled time, want 0", n)
	}
	if err := svc.Tick(); err != nil {
		t.Fatalf("early tick: %v", err)
	}
	if n := len(deliveries(t, db, a.ID)); n != 0 {
		t.Fatalf("%d deliveries after an early tick, want 0", n)
	}

	svc.now = func() time.Time { return at.Add(time.Minute) }
	if err := svc.Tick(); err != nil {
		t.Fatalf("due tick: %v", err)
	}
	if n := len(deliveries(t, db, a.ID)); n != 3 {
		t.Fatalf("%d deliveries after the scheduled time, want 3", n)
	}
}

func TestSyncUserBackfillsUsersWhoJoinedAfterTheBroadcast(t *testing.T) {
	svc, db, _ := newService(t)
	seedUsers(t, db)

	a := &models.Announcement{Title: "Policy change", Audience: models.AudienceAll}
	if err := svc.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	newcomer := models.User{ID: 5, Email: "new@example.com", Username: "new", Role: models.SystemRoleUser, Active: true}
	if err := db.Create(&newcomer).Error; err != nil {
		t.Fatalf("create newcomer: %v", err)
	}

	svc.SyncUser(5)
	got := deliveries(t, db, a.ID)
	if len(got) != 4 {
		t.Fatalf("%d deliveries after backfill, want 4", len(got))
	}

	// A second sync must not duplicate the delivery.
	svc.clearSyncMarks([]uint{5})
	svc.SyncUser(5)
	if n := len(deliveries(t, db, a.ID)); n != 4 {
		t.Fatalf("%d deliveries after a repeat sync, want 4", n)
	}

	var reloaded models.Announcement
	if err := db.First(&reloaded, a.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Recipients != 4 {
		t.Fatalf("recipients = %d, want 4 (the backfill is counted)", reloaded.Recipients)
	}
}

func TestSyncUserSkipsUsersOutsideTheAudience(t *testing.T) {
	svc, db, _ := newService(t)
	seedUsers(t, db)

	a := &models.Announcement{Title: "Admins only", Audience: models.AudienceAdmins}
	if err := svc.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	svc.SyncUser(3) // a plain user
	if n := len(deliveries(t, db, a.ID)); n != 1 {
		t.Fatalf("%d deliveries, want 1 — the backfill must respect the audience", n)
	}
}

func TestSyncUserIgnoresExpiredAnnouncements(t *testing.T) {
	svc, db, _ := newService(t)
	seedUsers(t, db)
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }

	past := now.Add(-time.Hour)
	a := &models.Announcement{Title: "Window closed", Audience: models.AudienceAll, ExpiresAt: &past}
	if err := svc.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	newcomer := models.User{ID: 6, Email: "late@example.com", Username: "late", Role: models.SystemRoleUser, Active: true}
	if err := db.Create(&newcomer).Error; err != nil {
		t.Fatalf("create newcomer: %v", err)
	}

	svc.SyncUser(6)
	var n int64
	if err := db.Model(&models.Notification{}).Where("announcement_id = ? AND user_id = ?", a.ID, 6).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatal("an expired announcement must not be backfilled")
	}
}

func TestUpdateRewritesEveryDelivery(t *testing.T) {
	svc, db, _ := newService(t)
	seedUsers(t, db)

	a := &models.Announcement{Title: "Maintenance at 02:00", Audience: models.AudienceAll, Severity: models.AlertInfo}
	if err := svc.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	a.Title = "Maintenance moved to 04:00"
	a.Severity = models.AlertWarning
	a.Pinned = true
	a.Dismissal = models.DismissNever
	if err := svc.Update(a); err != nil {
		t.Fatalf("update: %v", err)
	}

	for _, n := range deliveries(t, db, a.ID) {
		if n.Title != "Maintenance moved to 04:00" {
			t.Fatalf("delivery for user %d still reads %q", n.UserID, n.Title)
		}
		if n.Severity != models.AlertWarning || !n.Pinned {
			t.Fatalf("delivery for user %d kept severity %q pinned=%v", n.UserID, n.Severity, n.Pinned)
		}
		if n.Dismissal != models.DismissNever {
			t.Fatalf("delivery for user %d kept dismissal %q", n.UserID, n.Dismissal)
		}
	}
}

func TestUndismissableBannerSurvivesADismissRequest(t *testing.T) {
	svc, db, inbox := newService(t)
	seedUsers(t, db)
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }

	a := &models.Announcement{
		Title: "Demo environment", Audience: models.AudienceAll,
		Pinned: true, Dismissal: models.DismissNever,
	}
	if err := svc.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := inbox.Banners(2, now)
	if err != nil {
		t.Fatalf("banners: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d banners, want 1", len(got))
	}

	if err := inbox.Dismiss(2, []uint{got[0].ID}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	got, err = inbox.Banners(2, now)
	if err != nil {
		t.Fatalf("banners after dismiss: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d banners after a dismiss request, want the notice to stay", len(got))
	}

	// Unlocking it through an edit hands the close control back.
	a.Dismissal = models.DismissOnce
	if err := svc.Update(a); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := inbox.Dismiss(2, []uint{got[0].ID}); err != nil {
		t.Fatalf("dismiss after unlock: %v", err)
	}
	got, err = inbox.Banners(2, now)
	if err != nil {
		t.Fatalf("banners after unlock: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("%d banners after unlocking and dismissing, want 0", len(got))
	}
}

func TestRetractRemovesTheAnnouncementAndItsDeliveries(t *testing.T) {
	svc, db, _ := newService(t)
	seedUsers(t, db)

	a := &models.Announcement{Title: "Sent in error", Audience: models.AudienceAll}
	if err := svc.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Retract(a.ID); err != nil {
		t.Fatalf("retract: %v", err)
	}
	if n := len(deliveries(t, db, a.ID)); n != 0 {
		t.Fatalf("%d deliveries survived the retraction", n)
	}
	var count int64
	if err := db.Model(&models.Announcement{}).Where("id = ?", a.ID).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatal("the announcement row survived the retraction")
	}
}

func TestBannersExcludeDismissedAndExpired(t *testing.T) {
	svc, db, inbox := newService(t)
	seedUsers(t, db)
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }

	pinned := &models.Announcement{Title: "Read me", Audience: models.AudienceAll, Pinned: true}
	if err := svc.Create(pinned); err != nil {
		t.Fatalf("create pinned: %v", err)
	}
	past := now.Add(-time.Minute)
	lapsed := &models.Announcement{Title: "Over", Audience: models.AudienceAll, Pinned: true, ExpiresAt: &past}
	if err := svc.Create(lapsed); err != nil {
		t.Fatalf("create lapsed: %v", err)
	}

	got, err := inbox.Banners(2, now)
	if err != nil {
		t.Fatalf("banners: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Read me" {
		t.Fatalf("banners = %+v, want only the live pinned notice", got)
	}

	if err := inbox.Dismiss(2, []uint{got[0].ID}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	got, err = inbox.Banners(2, now)
	if err != nil {
		t.Fatalf("banners after dismiss: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("%d banners after dismissal, want 0", len(got))
	}

	// Dismissal is per-user: another recipient still sees it.
	other, err := inbox.Banners(3, now)
	if err != nil {
		t.Fatalf("banners for the other user: %v", err)
	}
	if len(other) != 1 {
		t.Fatalf("%d banners for the other user, want 1 — dismissal must not be shared", len(other))
	}
}

func TestSyncUserIsThrottledPerUser(t *testing.T) {
	svc, db, _ := newService(t)
	seedUsers(t, db)
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }

	a := &models.Announcement{Title: "Notice", Audience: models.AudienceAll}
	if err := svc.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	newcomer := models.User{ID: 7, Email: "seven@example.com", Username: "seven", Role: models.SystemRoleUser, Active: true}
	if err := db.Create(&newcomer).Error; err != nil {
		t.Fatalf("create newcomer: %v", err)
	}

	svc.SyncUser(7)
	if err := db.Where("announcement_id = ? AND user_id = ?", a.ID, 7).Delete(&models.Notification{}).Error; err != nil {
		t.Fatalf("delete delivery: %v", err)
	}
	svc.SyncUser(7) // within the interval: must not re-query
	var n int64
	if err := db.Model(&models.Notification{}).Where("announcement_id = ? AND user_id = ?", a.ID, 7).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatal("a second sync inside the throttle interval must not re-run")
	}

	svc.now = func() time.Time { return now.Add(2 * syncInterval) }
	svc.SyncUser(7)
	if err := db.Model(&models.Notification{}).Where("announcement_id = ? AND user_id = ?", a.ID, 7).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatal("a sync past the throttle interval must run")
	}
}
