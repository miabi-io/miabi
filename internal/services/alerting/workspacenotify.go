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
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

// WorkspaceNotifier delivers facts, not conditions: an Alert must later clear, and a success has nothing to clear.
type WorkspaceNotifier struct {
	members MemberLister
	inbox   InboxStore
	bus     Publisher
}

func NewWorkspaceNotifier(members MemberLister, inbox InboxStore, bus Publisher) *WorkspaceNotifier {
	return &WorkspaceNotifier{members: members, inbox: inbox, bus: bus}
}

// One row per member per call; the role gate is the tenancy boundary, as for alerts.
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
		if !m.Role.AtLeast(minRole) || m.User.IsService() {
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
