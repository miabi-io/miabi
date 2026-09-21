// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package alerting

import (
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

// WorkspaceNotifier delivers a standalone item to a workspace's members.
//
// It is the Engine's own fan-out — the same role gate, the same SSE push — separated from it so a
// caller reporting a FACT does not have to invent a CONDITION to carry it. An Alert is a small FSM:
// it fires, and something later has to clear it. "Last night's backup completed" has nothing to
// clear, and modelling it as an alert would leave a success firing in the list forever.
//
// AdminNotifier is the same idea aimed at the platform super-admins; this one is workspace-scoped.
type WorkspaceNotifier struct {
	members MemberLister
	inbox   InboxStore
	bus     Publisher
}

func NewWorkspaceNotifier(members MemberLister, inbox InboxStore, bus Publisher) *WorkspaceNotifier {
	return &WorkspaceNotifier{members: members, inbox: inbox, bus: bus}
}

// NotifyWorkspace writes one inbox row per member at or above minRole and pings each of their
// streams. A row per member per call: two finished backups are two facts, not one item updated
// twice.
//
// The role gate is the tenancy boundary, exactly as it is for alerts — a user only ever receives an
// item for a workspace they are in.
func (n *WorkspaceNotifier) NotifyWorkspace(workspaceID uint, minRole models.WorkspaceRole, item models.Notification) error {
	if n == nil || n.members == nil || n.inbox == nil || workspaceID == 0 {
		return nil
	}
	members, err := n.members.ListMembers(workspaceID)
	if err != nil {
		return err
	}
	if item.Kind == "" {
		item.Kind = models.NotificationKindInfo
	}
	if item.Severity == "" {
		item.Severity = models.AlertInfo
	}
	for _, m := range members {
		if !m.Role.AtLeast(minRole) {
			continue
		}
		row := item
		row.UserID = m.UserID
		row.WorkspaceID = workspaceID
		row.AlertID = nil
		if err := n.inbox.Upsert(&row, false); err != nil {
			return err
		}
		if n.bus != nil {
			n.bus.Publish(NotificationTopic(m.UserID), eventbus.Event{
				Type: "notification", Data: map[string]any{"user_id": m.UserID},
			})
		}
	}
	return nil
}
