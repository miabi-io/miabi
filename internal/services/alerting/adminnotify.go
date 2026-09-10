// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package alerting

import (
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

// AdminNotifier delivers a standalone item to every platform admin's inbox.
//
// It is the fan-out platform alerts already use — same recipients, same SSE push
// — separated from the Engine so a caller with something to say and no Alert
// behind it does not have to invent a second delivery path. A host-port binding
// waiting on review is the first such caller.
type AdminNotifier struct {
	admins SystemAdminLister
	inbox  InboxStore
	bus    Publisher
}

func NewAdminNotifier(admins SystemAdminLister, inbox InboxStore, bus Publisher) *AdminNotifier {
	return &AdminNotifier{admins: admins, inbox: inbox, bus: bus}
}

// NotifyAdmins writes one inbox row per admin and pings each of their streams.
// A row per admin per call: two pending requests are two things to act on, not
// one item updated twice.
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
