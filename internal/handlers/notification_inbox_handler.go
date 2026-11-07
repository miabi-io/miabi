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

package handlers

import (
	"strconv"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/alerting"
	"github.com/miabi-io/miabi/internal/services/announcement"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// A user never sees another user's or workspace's items.
type NotificationInboxHandler struct {
	repo          *repositories.NotificationInboxRepository
	bus           *eventbus.Bus
	announcements *announcement.Service
}

func NewNotificationInboxHandler(repo *repositories.NotificationInboxRepository, bus *eventbus.Bus, announcements *announcement.Service) *NotificationInboxHandler {
	return &NotificationInboxHandler{repo: repo, bus: bus, announcements: announcements}
}

// DismissRequest hides items from the app-wide banner.
type DismissRequest struct {
	Body struct {
		IDs []uint `json:"ids"`
	}
}

// Runs on read paths so users created later, or newly added to a targeted workspace, still see it.
func (h *NotificationInboxHandler) syncAnnouncements(userID uint) {
	if h.announcements != nil {
		h.announcements.SyncUser(userID)
	}
}

// MarkReadRequest marks specific notifications read.
type MarkReadRequest struct {
	Body struct {
		IDs []uint `json:"ids"`
	}
}

func (h *NotificationInboxHandler) List(c *okapi.Context) error {
	userID := middlewares.UserID(c)
	h.syncAnnouncements(userID)
	ws := uintQuery(c, "workspace")
	before := uintQuery(c, "before")
	limit := 30
	if q := c.Query("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	items, err := h.repo.ListByUser(userID, ws, c.Query("unread") == "true", before, limit)
	if err != nil {
		return c.AbortInternalServerError("failed to list notifications", err)
	}
	return ok(c, items)
}

// UnreadCount returns the bell badge count for the user.
func (h *NotificationInboxHandler) UnreadCount(c *okapi.Context) error {
	userID := middlewares.UserID(c)
	h.syncAnnouncements(userID)
	n, err := h.repo.UnreadCount(userID)
	if err != nil {
		return c.AbortInternalServerError("failed to count notifications", err)
	}
	return ok(c, map[string]int64{"unread": n})
}

// MarkRead marks the given notification ids read (ownership-scoped to the user).
func (h *NotificationInboxHandler) MarkRead(c *okapi.Context, req *MarkReadRequest) error {
	if err := h.repo.MarkRead(middlewares.UserID(c), req.Body.IDs); err != nil {
		return c.AbortInternalServerError("failed to mark read", err)
	}
	return ok(c, map[string]string{"message": "ok"})
}

func (h *NotificationInboxHandler) MarkAllRead(c *okapi.Context) error {
	if err := h.repo.MarkAllRead(middlewares.UserID(c), uintQuery(c, "workspace")); err != nil {
		return c.AbortInternalServerError("failed to mark all read", err)
	}
	return ok(c, map[string]string{"message": "ok"})
}

func (h *NotificationInboxHandler) Banners(c *okapi.Context) error {
	userID := middlewares.UserID(c)
	h.syncAnnouncements(userID)
	items, err := h.repo.Banners(userID, time.Now().UTC())
	if err != nil {
		return c.AbortInternalServerError("failed to list banners", err)
	}
	return ok(c, items)
}

// Dismiss hides the given items from the banner (ownership-scoped to the user).
func (h *NotificationInboxHandler) Dismiss(c *okapi.Context, req *DismissRequest) error {
	if err := h.repo.Dismiss(middlewares.UserID(c), req.Body.IDs); err != nil {
		return c.AbortInternalServerError("failed to dismiss", err)
	}
	return ok(c, map[string]string{"message": "ok"})
}

// The payload is only a nudge to refetch; Postgres stays the source of truth.
func (h *NotificationInboxHandler) Stream(c *okapi.Context) error {
	userID := middlewares.UserID(c)
	ch, unsubscribe := h.bus.Subscribe(alerting.NotificationTopic(userID))
	defer unsubscribe()

	ctx := c.Request().Context()
	msgs := make(chan okapi.Message)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-ch:
				if !ok {
					return
				}
				select {
				case msgs <- okapi.Message{Data: e, Serializer: okapi.JSONSerializer{}}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return c.SSEStreamWithOptions(ctx, msgs, &okapi.StreamOptions{PingInterval: 15 * time.Second})
}

func uintQuery(c *okapi.Context, name string) uint {
	if q := c.Query(name); q != "" {
		if n, err := strconv.ParseUint(q, 10, 64); err == nil {
			return uint(n)
		}
	}
	return 0
}
