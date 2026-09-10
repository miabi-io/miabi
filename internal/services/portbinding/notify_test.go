// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// A request nobody is told about waits until somebody thinks to look, which is
// how a one-click approval becomes a week of being blocked.
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
	// The admin has to be able to act without opening the request first, so the
	// port and the destination both belong in the notice.
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

// A privileged workspace's binding is approved on the spot. Announcing it would
// train admins to ignore the notice that matters.
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
