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

package portbinding

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

type capturingNotifier struct{ sent []models.Notification }

func (c *capturingNotifier) NotifyAdmins(n models.Notification) error {
	c.sent = append(c.sent, n)
	return nil
}

// An unannounced request waits until somebody thinks to look.
func TestPendingRequestNotifiesAdmins(t *testing.T) {
	notifier := &capturingNotifier{}
	s := &Service{notify: notifier}

	s.notifyPending(&models.PortBinding{
		ID: 3, ApplicationID: 9, HostPort: 8080, Protocol: "tcp",
		ContainerPort: 3000, Status: models.PortBindingPending,
	})

	if len(notifier.sent) != 1 {
		t.Fatalf("sent %d notices, want 1", len(notifier.sent))
	}
	n := notifier.sent[0]
	// Port and destination belong in the notice so the admin can act without opening the request.
	for _, want := range []string{"8080", "tcp"} {
		if !strings.Contains(n.Title, want) {
			t.Errorf("title %q does not mention %q", n.Title, want)
		}
	}
	if n.SubjectLink != "/admin/ports" {
		t.Errorf("link = %q, want the review page", n.SubjectLink)
	}
	if !strings.Contains(n.Body, "3000") {
		t.Errorf("body %q does not say which container port was asked for", n.Body)
	}
}

// Announcing auto-approved bindings would train admins to ignore the notice that matters.
func TestAutoApprovedBindingDoesNotNotify(t *testing.T) {
	notifier := &capturingNotifier{}
	s := &Service{notify: notifier}

	s.notifyPending(&models.PortBinding{ID: 1, Status: models.PortBindingApproved})

	if len(notifier.sent) != 0 {
		t.Errorf("sent %d notices for an auto-approved binding, want 0", len(notifier.sent))
	}
}

func TestNotifyPendingIsNilSafe(t *testing.T) {
	(&Service{}).notifyPending(&models.PortBinding{Status: models.PortBindingPending})
	(&Service{notify: &capturingNotifier{}}).notifyPending(nil)
}
