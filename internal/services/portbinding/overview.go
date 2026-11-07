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

package portbinding

import (
	"sort"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// Port states, as an admin needs to distinguish them.
const (
	// StatePending is a request awaiting review. Nothing is published.
	StatePending = "pending"
	// An approved host port is only published on the app's next deploy.
	StateReserved = "reserved"
	// StatePublished is approved and live on the node.
	StatePublished = "published"
	// Ports held by containers without a binding are invisible in the table and make the next approval fail.
	StateUnmanaged = "unmanaged"
)

// PortEntry is one host port on one node.
type PortEntry struct {
	HostPort      int       `json:"host_port"`
	Protocol      string    `json:"protocol"`
	State         string    `json:"state"`
	BindingID     uint      `json:"binding_id,omitempty"`
	WorkspaceID   uint      `json:"workspace_id,omitempty"`
	ApplicationID uint      `json:"application_id,omitempty"`
	AppName       string    `json:"app_name,omitempty"`
	ContainerPort int       `json:"container_port,omitempty"`
	Container     string    `json:"container,omitempty"`
	RequestedBy   uint      `json:"requested_by,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
}

// NodeOverview is one node's host-port map.
type NodeOverview struct {
	ServerID uint   `json:"server_id"`
	Name     string `json:"name"`
	// When false, entries come from the table alone; don't imply a clean node just because it was unreachable.
	Inspected bool        `json:"inspected"`
	Entries   []PortEntry `json:"entries"`
}

// Overview is the whole platform's external port surface.
type Overview struct {
	MinPort int            `json:"min_port"`
	MaxPort int            `json:"max_port"`
	Nodes   []NodeOverview `json:"nodes"`
}

// Optional: without it the overview uses ids as names.
type ServerLister interface {
	List() ([]models.Server, error)
}

// SetServers wires node naming for the admin overview (nil-safe).
func (s *Service) SetServers(l ServerLister) { s.servers = l }

// AppNamer resolves an application's name for display. Optional.
type AppNamer interface {
	FindByID(id uint) (*models.Application, error)
}

// Overview reports both disagreements: approved-but-not-redeployed bindings and ports held with no binding.
func (s *Service) Overview() (*Overview, error) {
	bindings, err := s.repo.ListActive()
	if err != nil {
		return nil, err
	}

	byNode := map[uint][]models.PortBinding{}
	for _, b := range bindings {
		byNode[b.ServerID] = append(byNode[b.ServerID], b)
	}

	names := map[uint]string{}
	var nodeIDs []uint
	if s.servers != nil {
		if servers, lerr := s.servers.List(); lerr == nil {
			for _, srv := range servers {
				names[srv.ID] = srv.Name
				nodeIDs = append(nodeIDs, srv.ID)
			}
		}
	}
	// A node with no Server row (id 0 = local) still needs a column, or its ports vanish.
	for id := range byNode {
		if _, known := names[id]; !known {
			nodeIDs = append(nodeIDs, id)
		}
	}
	if len(nodeIDs) == 0 {
		nodeIDs = []uint{0}
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	out := &Overview{MinPort: s.minPort, MaxPort: s.maxPort}
	for _, id := range nodeIDs {
		live, inspected := s.inspectNode(id)
		out.Nodes = append(out.Nodes, NodeOverview{
			ServerID:  id,
			Name:      s.nodeName(id, names),
			Inspected: inspected,
			Entries:   s.reconcile(byNode[id], live, inspected),
		})
	}
	return out, nil
}

func (s *Service) nodeName(id uint, names map[uint]string) string {
	if n, ok := names[id]; ok && n != "" {
		return n
	}
	if id == 0 {
		return "Local node"
	}
	return ""
}

func (s *Service) reconcile(bindings []models.PortBinding, live map[string]string, inspected bool) []PortEntry {
	entries := make([]PortEntry, 0, len(bindings)+len(live))
	claimed := map[string]bool{}

	for _, b := range bindings {
		key := portKey(b.HostPort, b.Protocol)
		claimed[key] = true
		e := PortEntry{
			HostPort: b.HostPort, Protocol: normProto(b.Protocol),
			BindingID: b.ID, WorkspaceID: b.WorkspaceID, ApplicationID: b.ApplicationID,
			ContainerPort: b.ContainerPort, RequestedBy: b.RequestedBy, CreatedAt: b.CreatedAt,
			Container: live[key],
		}
		switch {
		case b.Status == models.PortBindingPending:
			e.State = StatePending
		case !inspected:
			// The node could not be asked, so only reserved is certainly true.
			e.State = StateReserved
		case live[key] != "":
			e.State = StatePublished
		default:
			e.State = StateReserved
		}
		if s.apps != nil && b.ApplicationID != 0 {
			if app, err := s.apps.FindByID(b.ApplicationID); err == nil {
				e.AppName = app.Name
			}
		}
		entries = append(entries, e)
	}

	if inspected {
		for key, owner := range live {
			if claimed[key] {
				continue
			}
			port, proto := parsePortKey(key)
			entries = append(entries, PortEntry{
				HostPort: port, Protocol: proto, State: StateUnmanaged, Container: owner,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].HostPort != entries[j].HostPort {
			return entries[i].HostPort < entries[j].HostPort
		}
		return entries[i].Protocol < entries[j].Protocol
	})
	return entries
}
