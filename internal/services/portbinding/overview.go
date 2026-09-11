// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// StateReserved is approved but not yet on the node: an approved host port is
	// only published on the application's next deploy.
	StateReserved = "reserved"
	// StatePublished is approved and live on the node.
	StatePublished = "published"
	// StateUnmanaged is a host port held by a container Miabi has no binding for —
	// an imported container, one started by hand, or the gateway itself. These are
	// invisible in the binding table and are what makes the next approval fail.
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
	// Inspected reports whether the node's live ports could actually be read. When
	// false the entries come from the binding table alone: nothing can be said
	// about unmanaged ports, and reserved cannot be told from published. Saying so
	// beats implying the node is clean because it was unreachable.
	Inspected bool        `json:"inspected"`
	Entries   []PortEntry `json:"entries"`
}

// Overview is the whole platform's external port surface.
type Overview struct {
	MinPort int            `json:"min_port"`
	MaxPort int            `json:"max_port"`
	Nodes   []NodeOverview `json:"nodes"`
}

// ServerLister names the nodes a port may be published on. Optional: without it
// the overview still resolves, using ids as names.
type ServerLister interface {
	List() ([]models.Server, error)
}

// SetServers wires node naming for the admin overview (nil-safe).
func (s *Service) SetServers(l ServerLister) { s.servers = l }

// AppNamer resolves an application's name for display. Optional.
type AppNamer interface {
	FindByID(id uint) (*models.Application, error)
}

// Overview reconciles the binding table against what each node actually has
// published. The two disagree in both directions, and both disagreements matter:
// an approved binding is not live until the app redeploys, and a container can
// hold a host port with no binding at all.
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
	// A node with bindings but no Server row (id 0 = the local node) still needs a
	// column, or its ports vanish from the page.
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

// reconcile turns one node's bindings plus its live published ports into a
// single sorted list.
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
			// Approved, and the node could not be asked. Reporting "published" would
			// be a guess; reserved is the state that is certainly true.
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
