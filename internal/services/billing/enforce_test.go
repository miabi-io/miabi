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

package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/account"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/restriction"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type countingWorkloads struct{ stopped, started int }

func (w *countingWorkloads) StopWorkspace(context.Context, uint) (account.StopResult, models.FrozenResources) {
	w.stopped++
	return account.StopResult{}, models.FrozenResources{}
}

func (w *countingWorkloads) StartWorkspace(context.Context, uint, *models.FrozenResources) (account.StartResult, error) {
	w.started++
	return account.StartResult{}, nil
}

type eventSink struct {
	mu       sync.Mutex
	fail     bool
	received []Event
	headers  []http.Header
	bodies   [][]byte
}

func (e *eventSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var ev Event
	_ = json.Unmarshal(body, &ev)
	e.received = append(e.received, ev)
	e.headers = append(e.headers, r.Header.Clone())
	e.bodies = append(e.bodies, body)
	w.WriteHeader(http.StatusOK)
}

func TestEnforcement(t *testing.T) {
	ctx := context.Background()
	db := accountsDB(t)
	if err := db.AutoMigrate(&models.BillingEvent{}, &models.Setting{}, &models.APIKey{}); err != nil {
		t.Fatal(err)
	}
	crypto.Init("enforcement-test-secret-0123456789abcdef")
	wsRepo := repositories.NewWorkspaceRepository(db)
	workloads := &countingWorkloads{}
	restrictions := restriction.NewService(wsRepo, workloads)
	s := NewService(db, repositories.NewUserRepository(db), nil, repositories.NewPlanRepository(db), repositories.NewSettingRepository(db), EnrolmentInfo{})
	restrictions.SetBillingActor(s.ServiceUserID)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.SetAccountDeps(AccountDeps{Workspaces: fakeCreator{db}, Restrictions: restrictions, HardAfter: 14 * 24 * time.Hour})
	if err := db.Create(&models.Plan{Name: "pro", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := s.UpsertAccount(ctx, "cus_1", AccountUpsert{Owner: Owner{Email: "jane@acme.test"}, CreateWorkspace: &CreateWorkspace{Name: "acme"}}); err != nil {
		t.Fatal(err)
	}
	var acme models.Workspace
	db.Where("name = ?", "acme").First(&acme)
	// A second billed workspace an admin has put on hold: billing must leave that hold alone.
	held := models.Workspace{Name: "held", OwnerID: acme.OwnerID}
	db.Create(&held)
	db.Model(&models.Workspace{}).Where("id = ?", held.ID).Update("billing_account_id", *acme.BillingAccountID)
	if _, err := restrictions.Set(ctx, held.ID, 999, restriction.SetInput{State: models.WorkspaceRestrictionReadOnly, Reason: "abuse"}); err != nil {
		t.Fatal(err)
	}
	state := func(id uint) (models.WorkspaceRestriction, string) {
		var w models.Workspace
		db.First(&w, id)
		return w.Restriction, w.RestrictionReason
	}

	grace := now.Add(-time.Hour)
	t0 := now.Add(-time.Minute)
	if _, err := s.PushSubscription(ctx, "cus_1", SubscriptionPush{Plan: "pro", Status: "past_due", GraceUntil: &grace, EventAt: &t0}, false); err != nil {
		t.Fatal(err)
	}
	if st, reason := state(acme.ID); st != models.WorkspaceRestrictionReadOnly || reason != "payment is overdue" {
		t.Fatalf("past grace → read-only: %s %q", st, reason)
	}
	if err := restrictions.Allowed(acme.ID, restriction.ActionWrite); err == nil || err.(*restriction.Error).Code() != "BILLING_RESTRICTED" {
		t.Fatalf("billing hold must answer BILLING_RESTRICTED: %v", err)
	}
	if st, reason := state(held.ID); st != models.WorkspaceRestrictionReadOnly || reason != "abuse" {
		t.Fatalf("an admin's hold is left alone: %s %q", st, reason)
	}

	if _, err := s.SuspendAccount(ctx, "cus_1", SuspendRequest{Level: "hard", Reason: "fraud review"}); err != nil {
		t.Fatal(err)
	}
	if st, reason := state(acme.ID); st != models.WorkspaceRestrictionSuspended || reason != "fraud review" || workloads.stopped != 1 {
		t.Fatalf("forced hard → suspended: %s %q stopped=%d", st, reason, workloads.stopped)
	}
	if _, err := s.SuspendAccount(ctx, "cus_1", SuspendRequest{Level: "middling"}); err == nil {
		t.Error("an unknown level must be refused")
	}
	if _, err := s.ResumeAccount(ctx, "cus_1"); err != nil {
		t.Fatal(err)
	}
	if st, _ := state(acme.ID); st != models.WorkspaceRestrictionReadOnly || workloads.started != 1 {
		t.Fatalf("resume falls back to the subscription's level: %s started=%d", st, workloads.started)
	}

	// Time passes beyond grace + hard-after: the sweep suspends.
	now = grace.Add(15 * 24 * time.Hour)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := state(acme.ID); st != models.WorkspaceRestrictionSuspended {
		t.Fatalf("sweep past hard-after: %s", st)
	}

	t1 := now.Add(-time.Minute)
	if _, err := s.PushSubscription(ctx, "cus_1", SubscriptionPush{Plan: "pro", Status: "active", EventAt: &t1}, false); err != nil {
		t.Fatal(err)
	}
	if st, _ := state(acme.ID); st != models.WorkspaceRestrictionNone || workloads.started != 2 {
		t.Fatalf("paid → lifted: %s started=%d", st, workloads.started)
	}
	if st, _ := state(held.ID); st != models.WorkspaceRestrictionReadOnly {
		t.Fatalf("the admin's hold survives billing changes: %s", st)
	}

	var queued []models.BillingEvent
	db.Order("id").Find(&queued)
	var levels []string
	for _, ev := range queued {
		var d RestrictionChanged
		_ = json.Unmarshal([]byte(ev.Data), &d)
		levels = append(levels, d.From+">"+d.To)
	}
	want := []string{"none>soft", "soft>hard", "hard>soft", "soft>hard", "hard>none"}
	if len(levels) != len(want) {
		t.Fatalf("events: %v, want %v", levels, want)
	}
	for i := range want {
		if levels[i] != want[i] {
			t.Fatalf("events: %v, want %v", levels, want)
		}
	}

	// Not connected: events wait.
	if err := s.DeliverEvents(ctx); err != nil {
		t.Fatal(err)
	}
	sink := &eventSink{fail: true}
	srv := httptest.NewServer(sink)
	defer srv.Close()
	if err := s.storeConnection(Code{ConsoleURL: srv.URL, InstanceID: "inst-1"}, &enrollResponse{NotifyURL: srv.URL + "/events"}, "notify-secret", "portal", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverEvents(ctx); err == nil {
		t.Fatal("a failing console must report an error")
	}
	var first models.BillingEvent
	db.Order("id").First(&first)
	if first.Attempts != 1 || first.DeliveredAt != nil || !first.NextAttemptAt.After(now) {
		t.Fatalf("backoff after failure: %+v", first)
	}
	if err := s.DeliverEvents(ctx); err != nil || len(sink.received) != 0 {
		t.Fatalf("nothing is sent before the backoff ends: %v %d", err, len(sink.received))
	}

	sink.fail = false
	now = first.NextAttemptAt
	if err := s.DeliverEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.received) != len(want) {
		t.Fatalf("delivered %d, want %d", len(sink.received), len(want))
	}
	for i, ev := range sink.received {
		h := sink.headers[i]
		if ev.Sequence != queued[i].ID || ev.Type != EventRestrictionChanged || h.Get("X-Miabi-Delivery") != strconv.FormatInt(ev.Sequence, 10) ||
			h.Get("X-Miabi-Instance") != "inst-1" || h.Get("X-Miabi-Event") != EventRestrictionChanged || h.Get("X-Miabi-Signature") != Sign("notify-secret", sink.bodies[i]) {
			t.Fatalf("event %d: %+v %v", i, ev, h)
		}
	}
	var pending int64
	db.Model(&models.BillingEvent{}).Where("delivered_at IS NULL").Count(&pending)
	if pending != 0 {
		t.Fatalf("%d events still pending", pending)
	}
}
