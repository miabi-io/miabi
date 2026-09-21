// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package alerting

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

type stubMembers struct{ members []models.WorkspaceMember }

func (s stubMembers) ListMembers(uint) ([]models.WorkspaceMember, error) { return s.members, nil }

type capturedInbox struct{ rows []models.Notification }

func (c *capturedInbox) Upsert(n *models.Notification, _ bool) error {
	c.rows = append(c.rows, *n)
	return nil
}
func (c *capturedInbox) ApplyAlertUpdate(uint, models.Notification, bool) ([]uint, error) {
	return nil, nil
}

type capturedBus struct{ topics []string }

func (c *capturedBus) Publish(topic string, _ eventbus.Event) { c.topics = append(c.topics, topic) }

func members() []models.WorkspaceMember {
	return []models.WorkspaceMember{
		{UserID: 1, Role: models.WorkspaceRoleOwner},
		{UserID: 2, Role: models.WorkspaceRoleDeveloper},
		{UserID: 3, Role: models.WorkspaceRoleViewer},
	}
}

// The role gate is the whole boundary: a viewer cannot act on a failed backup, so they are not told
// about it, exactly as they are not told about the alert.
func TestNotifyWorkspaceRespectsMinRole(t *testing.T) {
	inbox, bus := &capturedInbox{}, &capturedBus{}
	n := NewWorkspaceNotifier(stubMembers{members()}, inbox, bus)

	err := n.NotifyWorkspace(9, models.WorkspaceRoleDeveloper, models.Notification{
		Title: "Recovery point completed — pg", Category: models.CategoryDatabase,
	})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if len(inbox.rows) != 2 {
		t.Fatalf("delivered to %d members, want the owner and the developer only", len(inbox.rows))
	}
	for _, r := range inbox.rows {
		if r.UserID == 3 {
			t.Error("a viewer received a developer-level item")
		}
		if r.WorkspaceID != 9 {
			t.Errorf("row workspace = %d, want 9", r.WorkspaceID)
		}
		if r.Kind != models.NotificationKindInfo {
			t.Errorf("kind = %q, want %q", r.Kind, models.NotificationKindInfo)
		}
		if r.Severity != models.AlertInfo {
			t.Errorf("severity = %q, want it defaulted to info", r.Severity)
		}
		// An AlertID would let Upsert fold this into an alert's notification and make a standalone
		// fact part of a condition's history.
		if r.AlertID != nil {
			t.Error("a standalone item carries an alert id")
		}
	}
	if len(bus.topics) != 2 {
		t.Errorf("pushed %d streams, want one per recipient", len(bus.topics))
	}
	if bus.topics[0] != NotificationTopic(1) {
		t.Errorf("topic = %q, want %q", bus.topics[0], NotificationTopic(1))
	}
}

// An explicit severity survives — a failed run is reported as a warning, not an info.
func TestNotifyWorkspaceKeepsAnExplicitSeverity(t *testing.T) {
	inbox := &capturedInbox{}
	n := NewWorkspaceNotifier(stubMembers{members()}, inbox, nil)

	if err := n.NotifyWorkspace(9, models.WorkspaceRoleOwner, models.Notification{
		Title: "Backup failed — orders", Severity: models.AlertWarning,
	}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if len(inbox.rows) != 1 {
		t.Fatalf("delivered %d rows, want 1 (the owner)", len(inbox.rows))
	}
	if inbox.rows[0].Severity != models.AlertWarning {
		t.Errorf("severity = %q, want warning", inbox.rows[0].Severity)
	}
}

// Workspace 0 is not a workspace. Delivering there would leak an item into every member list.
func TestNotifyWorkspaceIgnoresWorkspaceZero(t *testing.T) {
	inbox := &capturedInbox{}
	n := NewWorkspaceNotifier(stubMembers{members()}, inbox, nil)
	if err := n.NotifyWorkspace(0, models.WorkspaceRoleViewer, models.Notification{Title: "x"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if len(inbox.rows) != 0 {
		t.Errorf("delivered %d rows for workspace 0, want 0", len(inbox.rows))
	}
}
