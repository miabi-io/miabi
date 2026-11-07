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

// Package notify fans persisted application events out to a workspace's webhooks and channels.
package notify

import (
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// Defined here because worker imports this package for the sender registry.
type Enqueuer interface {
	EnqueueNotifyFanout(appEventID uint) error
}

// Dispatcher enqueues one fan-out task per event; resolution and delivery happen in the worker.
type Dispatcher struct {
	enqueuer Enqueuer
}

func NewDispatcher(enqueuer Enqueuer) *Dispatcher { return &Dispatcher{enqueuer: enqueuer} }

// OnEvent is best-effort: failures are logged, never propagated to the recorder.
func (d *Dispatcher) OnEvent(e *models.AppEvent) {
	if d == nil || d.enqueuer == nil || e == nil || e.ID == 0 {
		return
	}
	if err := d.enqueuer.EnqueueNotifyFanout(e.ID); err != nil {
		logger.Error("failed to enqueue notification fan-out", "event", e.ID, "type", e.Type, "error", err)
	}
}
