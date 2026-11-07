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

package alerting

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

type recordingInbox struct {
	rows []models.Notification
	err  error
}

func (r *recordingInbox) Upsert(n *models.Notification, _ bool) error {
	if r.err != nil {
		return r.err
	}
	r.rows = append(r.rows, *n)
	return nil
}
func (r *recordingInbox) ApplyAlertUpdate(uint, models.Notification, bool) ([]uint, error) {
	return nil, nil
}

type failingAdmins struct{ err error }

func (f failingAdmins) ListAdminIDs() ([]uint, error) { return nil, f.err }

func TestNotifyAdminsReachesEveryAdmin(t *testing.T) {
	inbox := &recordingInbox{}
	bus := &fakeBus{pushes: map[string]int{}}
	n := NewAdminNotifier(fakeAdmins{ids: []uint{1, 4, 9}}, inbox, bus)

	if err := n.NotifyAdmins(models.Notification{Title: "Host port 8080/tcp awaiting review"}); err != nil {
		t.Fatalf("NotifyAdmins: %v", err)
	}
	if len(inbox.rows) != 3 {
		t.Fatalf("wrote %d rows, want one per admin", len(inbox.rows))
	}
	seen := map[uint]bool{}
	for _, r := range inbox.rows {
		seen[r.UserID] = true
		if r.Kind != models.NotificationKindInfo {
			t.Errorf("kind = %q, want the standalone info kind", r.Kind)
		}
		// An AlertID would make the inbox update one row, collapsing separate requests into one item.
		if r.AlertID != nil {
			t.Error("a standalone notice must not carry an AlertID")
		}
	}
	for _, uid := range []uint{1, 4, 9} {
		if !seen[uid] {
			t.Errorf("admin %d got nothing", uid)
		}
		// The bell only refreshes when the user's own stream is pinged.
		if bus.pushes[NotificationTopic(uid)] != 1 {
			t.Errorf("admin %d got %d pushes, want 1", uid, bus.pushes[NotificationTopic(uid)])
		}
	}
}

func TestNotifyAdminsIsNilSafe(t *testing.T) {
	var n *AdminNotifier
	if err := n.NotifyAdmins(models.Notification{}); err != nil {
		t.Errorf("nil notifier returned %v, want nil", err)
	}
	if err := NewAdminNotifier(nil, nil, nil).NotifyAdmins(models.Notification{}); err != nil {
		t.Errorf("unwired notifier returned %v, want nil", err)
	}
}

func TestNotifyAdminsSurfacesFailures(t *testing.T) {
	boom := errors.New("db down")
	if err := NewAdminNotifier(failingAdmins{boom}, &recordingInbox{}, &fakeBus{pushes: map[string]int{}}).
		NotifyAdmins(models.Notification{}); !errors.Is(err, boom) {
		t.Errorf("want the lister's error, got %v", err)
	}
	if err := NewAdminNotifier(fakeAdmins{ids: []uint{1}}, &recordingInbox{err: boom}, &fakeBus{pushes: map[string]int{}}).
		NotifyAdmins(models.Notification{}); !errors.Is(err, boom) {
		t.Errorf("want the inbox's error, got %v", err)
	}
}
