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

// AdminNotifier reuses the platform-alert fan-out for standalone items with no Alert behind them.
type AdminNotifier struct {
	admins SystemAdminLister
	inbox  InboxStore
	bus    Publisher
}

func NewAdminNotifier(admins SystemAdminLister, inbox InboxStore, bus Publisher) *AdminNotifier {
	return &AdminNotifier{admins: admins, inbox: inbox, bus: bus}
}

// A row per admin per call: two pending requests are two things to act on, not one item updated twice.
func (n *AdminNotifier) NotifyAdmins(item models.Notification) error {
	if n == nil || n.admins == nil || n.inbox == nil {
		return nil
	}
	admins, err := n.admins.ListAdminIDs()
	if err != nil {
		return err
	}
	if item.Kind == "" {
		item.Kind = models.NotificationKindInfo
	}
	for _, uid := range admins {
		row := item
		row.UserID = uid
		if err := n.inbox.Upsert(&row, false); err != nil {
			return err
		}
		if n.bus != nil {
			n.bus.Publish(NotificationTopic(uid), eventbus.Event{
				Type: "notification", Data: map[string]any{"user_id": uid},
			})
		}
	}
	return nil
}
