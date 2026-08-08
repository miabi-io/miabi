// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/events"
	"github.com/miabi-io/miabi/internal/services/monitoring"
)

// EventsHandler serves the application timeline (events) and runtime container
// logs. Both are workspace-scoped; SSE endpoints accept a query-token.
type EventsHandler struct {
	events     *events.Service
	bus        *eventbus.Bus
	apps       *application.Service
	monitoring *monitoring.Service
}

func NewEventsHandler(ev *events.Service, bus *eventbus.Bus, apps *application.Service, mon *monitoring.Service) *EventsHandler {
	return &EventsHandler{events: ev, bus: bus, apps: apps, monitoring: mon}
}

// List returns an application's events (newest first, cursor via ?before=).
func (h *EventsHandler) List(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	before, _ := strconv.ParseUint(c.Query("before"), 10, 64)
	list, err := h.events.List(app.ID, limit, uint(before))
	if err != nil {
		return c.AbortInternalServerError("failed to list events", err)
	}
	return ok(c, list)
}

// WorkspaceEvent is an application event enriched with its application's
// name/slug so the workspace-wide events feed can show where each event belongs.
type WorkspaceEvent struct {
	models.AppEvent
	AppName        string `json:"app_name"`         // unique slug handle
	AppDisplayName string `json:"app_display_name"` // free-text label
}

// WorkspaceList returns the workspace's application events across all apps,
// paginated (page/size), newest first.
func (h *EventsHandler) WorkspaceList(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	list, total, err := h.events.ListByWorkspace(wsID, c.Query("order"), c.Query("severity"), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list events", err)
	}
	// Enrich with application names in one pass over the workspace's apps.
	names := map[uint]models.Application{}
	if apps, err := h.apps.List(wsID); err == nil {
		for _, a := range apps {
			names[a.ID] = a
		}
	}
	out := make([]WorkspaceEvent, 0, len(list))
	for _, e := range list {
		we := WorkspaceEvent{AppEvent: e}
		if a, ok := names[e.ApplicationID]; ok {
			we.AppName, we.AppDisplayName = a.Name, a.DisplayName
		}
		out = append(out, we)
	}
	return paginated(c, out, total, page, size)
}

// WorkspaceStream pushes live events for every application in the workspace over SSE — the
// dashboard's activity and health feed. SSEStreamWithOptions flushes headers immediately so
// EventSource fires onopen, and emits a periodic ":ping" to hold idle connections open.
func (h *EventsHandler) WorkspaceStream(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	ch, unsubscribe := h.bus.Subscribe(events.WorkspaceTopic(wsID))
	defer unsubscribe()

	ctx := c.Request().Context()
	// Bridge bus events into the SSE channel. Empty Event field + JSON serializer
	// reproduces the wire format the dashboard parses ({type:"event",data:{…}}
	// delivered to EventSource.onmessage).
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

// Stream pushes live events for an application over SSE.
func (h *EventsHandler) Stream(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	ch, unsubscribe := h.bus.Subscribe(events.Topic(app.ID))
	defer unsubscribe()

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			_ = c.SSESendJSON(e)
		}
	}
}

// LogsStream streams the active container's runtime logs over SSE.
func (h *EventsHandler) LogsStream(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	// follow defaults to true (live tail — the web console's behaviour); pass
	// ?follow=false for a one-shot snapshot of the last `tail` lines.
	follow := c.Query("follow") != "false"
	err = h.monitoring.StreamAppLogs(c.Request().Context(), app.WorkspaceID, app.ID, follow, c.Query("tail"), func(l docker.LogLine) error {
		return c.SSESendJSON(l)
	})
	if errors.Is(err, monitoring.ErrNoActiveContainer) {
		return c.AbortWithError(409, err)
	}
	return err
}

func (h *EventsHandler) load(c *okapi.Context) (*models.Application, error) {
	appID, err := strconv.Atoi(c.Param("appID"))
	if err != nil || appID <= 0 {
		return nil, errors.New("invalid app id")
	}
	return h.apps.Get(middlewares.WorkspaceID(c), uint(appID))
}
