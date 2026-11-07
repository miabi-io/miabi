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
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

// Each stream filters it down to the runners its viewer may see.
const runnerStatusTopic = "runners:status"

// SetEventBus wires the bus runner status events travel on (nil disables live status).
func (h *RunnerHandler) SetEventBus(bus *eventbus.Bus) { h.bus = bus }

// SetEventBus wires the bus the shared pool's status stream subscribes to.
func (h *AdminRunnerHandler) SetEventBus(bus *eventbus.Bus) { h.base.SetEventBus(bus) }

// Carries the whole runner, so a just-connected row also shows its version and platform.
func (h *RunnerHandler) PublishStatus(id uint, online bool) {
	if h.bus == nil {
		return
	}
	r, err := h.svc.Get(id)
	if err != nil {
		return
	}
	r.Connected = online
	h.bus.Publish(runnerStatusTopic, eventbus.Event{Type: "status", Data: *r})
}

// StatusEvents streams the workspace's own runners connecting and disconnecting (SSE).
func (h *RunnerHandler) StatusEvents(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	return h.streamStatus(c,
		func() ([]models.Runner, error) { return h.svc.ListWorkspace(wsID) },
		func(r *models.Runner) bool { return r.WorkspaceID != nil && *r.WorkspaceID == wsID },
	)
}

// StatusEvents streams the shared pool's runners connecting and disconnecting (SSE).
func (h *AdminRunnerHandler) StatusEvents(c *okapi.Context) error {
	return h.base.streamStatus(c, h.svc.ListShared, func(r *models.Runner) bool { return r.WorkspaceID == nil })
}

// Opens with a snapshot so a page that connects late or reconnects needs no refetch.
func (h *RunnerHandler) streamStatus(c *okapi.Context, list func() ([]models.Runner, error), visible func(*models.Runner) bool) error {
	var (
		ch          <-chan eventbus.Event
		unsubscribe = func() {}
	)
	// Subscribe before reading the snapshot, so a runner connecting in between is not missed.
	if h.bus != nil {
		ch, unsubscribe = h.bus.Subscribe(runnerStatusTopic)
	}
	defer unsubscribe()

	runners, err := list()
	if err != nil {
		return c.AbortInternalServerError("failed to list runners", err)
	}
	h.annotate(runners)
	if err := c.SSESendJSON(eventbus.Event{Type: "snapshot", Data: runners}); err != nil {
		return err
	}

	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case e, open := <-ch:
			if !open {
				return nil
			}
			r, ok := e.Data.(models.Runner)
			if !ok || !visible(&r) {
				continue
			}
			if err := c.SSESendJSON(e); err != nil {
				return err
			}
		}
	}
}
