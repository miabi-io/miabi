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

package cluster

import (
	"fmt"
	"strings"
)

// NodeAction is what a refused node was about to do.
type NodeAction int

const (
	// EnableSwarmAction initializes a swarm on the node.
	EnableSwarmAction NodeAction = iota
	// JoinAction joins the node to a swarm other than the default cluster.
	JoinAction
	// LeaveAction takes the node out of such a swarm.
	LeaveAction
)

// Matches ErrNodeHasWorkloads.
type NodeWorkloadsError struct {
	Node      string
	Action    NodeAction
	Apps      int64
	Databases int64
	Volumes   int64
}

func (e *NodeWorkloadsError) Is(target error) bool { return target == ErrNodeHasWorkloads }

func (e *NodeWorkloadsError) Error() string {
	runs := fmt.Sprintf("node %q runs %s", e.Node, e.inventory())
	switch e.Action {
	case EnableSwarmAction:
		return runs + ", so Swarm cannot be enabled on it: a cluster other than the default one only takes empty " +
			"nodes. Join the node to the default cluster instead, which accepts nodes with workloads, or enable " +
			"Swarm on an empty node in this location"
	case JoinAction:
		return runs + ", so it cannot join this cluster: a swarm other than the default cluster only takes empty " +
			"nodes. Join it to the default cluster instead, which accepts nodes with workloads, or remove them first"
	default:
		return runs + ", so it cannot leave this cluster: they rely on the cluster's overlay networks. Remove them first"
	}
}

func (e *NodeWorkloadsError) inventory() string {
	var parts []string
	for _, c := range []struct {
		n    int64
		noun string
	}{{e.Apps, "app"}, {e.Databases, "database"}, {e.Volumes, "volume"}} {
		switch {
		case c.n == 1:
			parts = append(parts, "1 "+c.noun)
		case c.n > 1:
			parts = append(parts, fmt.Sprintf("%d %ss", c.n, c.noun))
		}
	}
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func (s *Service) requireEmptyNode(serverID uint, action NodeAction) error {
	if s.store == nil {
		return nil
	}
	apps, dbs, vols, err := s.store.CountServerWorkloadsByKind(serverID)
	if err != nil {
		return err
	}
	if apps+dbs+vols == 0 {
		return nil
	}
	name := fmt.Sprintf("node %d", serverID)
	if srv, err := s.nodes.Get(serverID); err == nil && srv.Label() != "" {
		name = srv.Label()
	}
	return &NodeWorkloadsError{Node: name, Action: action, Apps: apps, Databases: dbs, Volumes: vols}
}
