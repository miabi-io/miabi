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

package netfabric

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// ReservedReleaser frees a reserved pool subnet. Satisfied by *repositories.NetworkAllocationRepository.
type ReservedReleaser interface {
	ReleaseReserved(subnet string) error
}

func (s *Service) SetReservedReleaser(r ReservedReleaser) { s.releaser = r }

type RemoveResult struct {
	Network string `json:"network"`
	Removed bool   `json:"removed"`
	// Released is true when the network's subnet was a reserved pool range, now free to hand out again.
	Released bool   `json:"released,omitempty"`
	Error    string `json:"error,omitempty"`
}

var (
	ErrNotOrphan     = errors.New("not an orphaned Miabi network: a workspace, stack or the pool still claims it")
	ErrNotMiabis     = errors.New("Miabi did not create this network, so it is never removed from here")
	ErrNetworkInUse  = errors.New("containers still reference this network")
	ErrNetworkAbsent = errors.New("the network no longer exists on this node")
)

// RemoveOrphans re-checks each network against live records before removing it, so a stale report never deletes a live one.
func (s *Service) RemoveOrphans(ctx context.Context, nodeID uint, names []string) ([]RemoveResult, error) {
	k, err := s.gather(s.expect())
	if err != nil {
		return nil, err
	}
	dc, err := s.clients.For(nodeID)
	if err != nil {
		return nil, err
	}
	nets, err := dc.ListNetworks(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]docker.Network, len(nets))
	for _, n := range nets {
		byName[n.Name] = n
	}
	out := make([]RemoveResult, 0, len(names))
	for _, name := range names {
		res := RemoveResult{Network: name}
		n, ok := byName[name]
		if err := s.removeOrphan(ctx, dc, n, ok, k); err != nil {
			res.Error = err.Error()
		} else {
			res.Removed = true
			if n.Subnet != "" && k.reserved[n.Subnet] && s.releaser != nil {
				res.Released = s.releaser.ReleaseReserved(n.Subnet) == nil
			}
		}
		out = append(out, res)
	}
	return out, nil
}

func (s *Service) removeOrphan(ctx context.Context, dc docker.Client, n docker.Network, exists bool, k *known) error {
	switch {
	case !exists:
		return ErrNetworkAbsent
	case n.Labels[docker.ManagedLabel] != "true":
		return ErrNotMiabis
	case !isOrphan(n, k):
		return ErrNotOrphan
	}
	users, err := dc.ContainersOnNetwork(ctx, n.Name)
	if err != nil {
		return fmt.Errorf("list the network's containers: %w", err)
	}
	if len(users) > 0 {
		return fmt.Errorf("%w: %s", ErrNetworkInUse, strings.Join(users, ", "))
	}
	return dc.RemoveNetwork(ctx, n.Name)
}

func isOrphan(n docker.Network, k *known) bool {
	if n.Driver != "bridge" || (!strings.HasPrefix(n.Name, "mb-ws") && !strings.HasPrefix(n.Name, "mb-stack-")) {
		return false
	}
	_, isWorkspace := k.models[n.Name]
	_, allocated := k.ledger[n.Name]
	return !isWorkspace && !k.stacks[n.Name] && !allocated
}
