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
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

// One topic, not one per node: the node list watches all of them and a detail page filters by id.
const nodeStatusTopic = "nodes:status"

type NodeStatusEvent struct {
	ID           uint       `json:"id"`
	Name         string     `json:"name"`
	Online       bool       `json:"online"`
	Status       string     `json:"status"`
	AgentVersion string     `json:"agent_version,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	// A reconnect is when an agent's version can change (an upgrade is a restart), so the badge updates live.
	AgentState         string `json:"agent_state,omitempty"`
	AgentLatestVersion string `json:"agent_latest_version,omitempty"`
}

// Called by the node manager only on a transition.
func (h *NodeHandler) PublishStatus(nodeID uint, name string, online bool) {
	if h.bus == nil {
		return
	}
	h.bus.Publish(nodeStatusTopic, eventbus.Event{Type: "status", Data: h.statusOf(nodeID, name, online)})
}

// An unreadable record still yields the transition itself, which the console cannot do without.
func (h *NodeHandler) statusOf(nodeID uint, name string, online bool) NodeStatusEvent {
	ev := NodeStatusEvent{ID: nodeID, Name: name, Online: online, Status: string(models.ServerStatusOffline)}
	if online {
		ev.Status = string(models.ServerStatusOnline)
	}
	srv, err := h.nodes.Get(nodeID)
	if err != nil {
		return ev
	}
	if ev.Name == "" {
		ev.Name = srv.DisplayName
	}
	ev.AgentVersion, ev.LastSeenAt = srv.AgentVersion, srv.LastSeenAt
	one := []models.Server{*srv}
	h.annotateAgentVersions(one)
	ev.AgentState, ev.AgentLatestVersion = one[0].AgentState, one[0].AgentLatestVersion
	return ev
}

// Opens with a snapshot so a page that connects late or reconnects needs no refetch.
func (h *NodeHandler) StatusEvents(c *okapi.Context) error {
	if err := h.sendStatusSnapshot(c); err != nil {
		return err
	}
	if h.bus == nil {
		<-c.Request().Context().Done()
		return nil
	}
	ch, unsubscribe := h.bus.Subscribe(nodeStatusTopic)
	defer unsubscribe()
	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case e, open := <-ch:
			if !open {
				return nil
			}
			if err := c.SSESendJSON(e); err != nil {
				return err
			}
		}
	}
}

// sendStatusSnapshot opens the stream with where every node stands right now.
func (h *NodeHandler) sendStatusSnapshot(c *okapi.Context) error {
	servers, err := h.nodes.List(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to list nodes", err)
	}
	h.annotateAgentVersions(servers)
	events := make([]NodeStatusEvent, 0, len(servers))
	for i := range servers {
		s := &servers[i]
		online := s.IsLocal || h.manager.Connected(s.ID)
		events = append(events, NodeStatusEvent{
			ID: s.ID, Name: s.DisplayName, Online: online, Status: string(s.Status),
			AgentVersion: s.AgentVersion, LastSeenAt: s.LastSeenAt,
			AgentState: s.AgentState, AgentLatestVersion: s.AgentLatestVersion,
		})
	}
	return c.SSESendJSON(eventbus.Event{Type: "snapshot", Data: events})
}
