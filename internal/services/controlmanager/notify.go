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

package controlmanager

import (
	"context"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
)

const (
	// Covers event delivery and an operator swapping a container by hand; Miabi's own removals write intent first.
	checkGrace = 10 * time.Second
	// loopWindow and loopRestores open the breaker on something that keeps removing an app outside Miabi.
	loopWindow   = 15 * time.Minute
	loopRestores = 3
	// A container that exits again this soon after a start was not really brought back.
	reExitWindow = 30 * time.Second
)

// Leader reports whether this process leads the control plane. Satisfied by the cron manager.
type Leader interface {
	Leading() bool
}

// SetLeader keeps event checks on the process that sweeps, which holds the agent tunnels.
func (s *Service) SetLeader(l Leader) { s.leader = l }

// Notify is a hint that an app's active container was removed outside Miabi; it never blocks the event stream.
func (s *Service) Notify(appID uint) {
	if s.leader != nil && !s.leader.Leading() {
		return
	}
	if s.Mode() == ModeOff {
		return
	}
	s.mu.Lock()
	if s.pending[appID] {
		s.mu.Unlock()
		return
	}
	s.pending[appID] = true
	s.mu.Unlock()

	s.after(checkGrace, func() {
		s.mu.Lock()
		delete(s.pending, appID)
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
		defer cancel()
		if err := s.Check(ctx, appID); err != nil {
			logger.Warn("control manager: checking an app after its container was removed failed", "app_id", appID, "error", err)
		}
	})
}

// Caller holds the lock. Reports whether this restore makes one too many inside the window.
func (a *attempt) loopedLocked(now time.Time) bool {
	kept := a.eventRestores[:0]
	for _, at := range a.eventRestores {
		if now.Sub(at) < loopWindow {
			kept = append(kept, at)
		}
	}
	a.eventRestores = append(kept, now)
	return len(a.eventRestores) >= loopRestores
}

// startAgain starts the same container, then counts the attempt as failed if it does not stay up.
func (s *Service) startAgain(ctx context.Context, d actionable) error {
	dc, err := s.nodes.For(d.it.nodeID)
	if err != nil {
		return err
	}
	if err := dc.StartContainer(ctx, d.it.containerID); err != nil {
		return err
	}
	s.after(reExitWindow, func() {
		ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
		defer cancel()
		c, err := dc.InspectContainer(ctx, d.it.containerID)
		if err == nil && c.State == "running" {
			return
		}
		s.failed(d, fmt.Errorf("the container exited again within %s of being started", reExitWindow))
	})
	return nil
}
