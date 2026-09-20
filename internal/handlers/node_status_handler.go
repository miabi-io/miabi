// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

// A node's reachability reaches the console as it changes, rather than on the next reload. Adding a
// node is the case that needs it: an agent comes up seconds or minutes after the operator pastes
// the join command, and before this there was nothing to watch but a static page.

// nodeStatusTopic carries every node's transitions. It is one topic rather than one per node
// because the console's node list watches all of them at once, and a detail page filters by id.
const nodeStatusTopic = "nodes:status"

// NodeStatusEvent is a node's live reachability. Online is what the node list renders; the rest is
// what the row shows beside it, so a connecting node fills in without a refetch.
type NodeStatusEvent struct {
	ID           uint       `json:"id"`
	Name         string     `json:"name"`
	Online       bool       `json:"online"`
	Status       string     `json:"status"`
	AgentVersion string     `json:"agent_version,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
}

// PublishStatus fans a node's connect/disconnect out to the console. Registered as a listener on
// the node manager, which only calls it on a transition.
func (h *NodeHandler) PublishStatus(nodeID uint, name string, online bool) {
	if h.bus == nil {
		return
	}
	h.bus.Publish(nodeStatusTopic, eventbus.Event{Type: "status", Data: h.statusOf(nodeID, name, online)})
}

// statusOf builds the event, enriching it from the node record. A record that cannot be read still
// yields the transition itself, which is the part the console cannot do without.
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
	return ev
}

// StatusEvents streams every node's reachability over SSE. It opens with a snapshot of all nodes,
// so a page that connects late — or reconnects after a drop — is correct without also refetching.
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
	events := make([]NodeStatusEvent, 0, len(servers))
	for i := range servers {
		s := &servers[i]
		online := s.IsLocal || h.manager.Connected(s.ID)
		events = append(events, NodeStatusEvent{
			ID: s.ID, Name: s.DisplayName, Online: online, Status: string(s.Status),
			AgentVersion: s.AgentVersion, LastSeenAt: s.LastSeenAt,
		})
	}
	return c.SSESendJSON(eventbus.Event{Type: "snapshot", Data: events})
}
