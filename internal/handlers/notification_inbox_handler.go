// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strconv"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/alerting"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// NotificationInboxHandler serves a user's cross-workspace notification inbox —
// the dashboard bell and Notifications page. Everything is scoped to the
// authenticated user; a user never sees another user's (or workspace's) items.
type NotificationInboxHandler struct {
	repo *repositories.NotificationInboxRepository
	bus  *eventbus.Bus
}

func NewNotificationInboxHandler(repo *repositories.NotificationInboxRepository, bus *eventbus.Bus) *NotificationInboxHandler {
	return &NotificationInboxHandler{repo: repo, bus: bus}
}

// MarkReadRequest marks specific notifications read.
type MarkReadRequest struct {
	Body struct {
		IDs []uint `json:"ids"`
	}
}

// List returns the user's notifications (newest first). Query: workspace (0=all),
// unread (bool), before (id cursor), limit.
func (h *NotificationInboxHandler) List(c *okapi.Context) error {
	userID := middlewares.UserID(c)
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
	n, err := h.repo.UnreadCount(middlewares.UserID(c))
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

// MarkAllRead marks every unread notification read, optionally scoped to one
// workspace (?workspace=).
func (h *NotificationInboxHandler) MarkAllRead(c *okapi.Context) error {
	if err := h.repo.MarkAllRead(middlewares.UserID(c), uintQuery(c, "workspace")); err != nil {
		return c.AbortInternalServerError("failed to mark all read", err)
	}
	return ok(c, map[string]string{"message": "ok"})
}

// Stream pushes a live signal whenever the user's inbox changes (new alert, count
// bump, auto-resolve). The payload is a nudge to refetch the list + unread count;
// Postgres stays the source of truth. Same SSE mechanism as the activity feed.
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

// uintQuery parses a non-negative integer query param, 0 when absent/invalid.
func uintQuery(c *okapi.Context, name string) uint {
	if q := c.Query(name); q != "" {
		if n, err := strconv.ParseUint(q, 10, 64); err == nil {
			return uint(n)
		}
	}
	return 0
}
