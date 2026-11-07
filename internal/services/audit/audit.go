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

// Package audit writes append-only audit-log entries for mutating actions.
package audit

import (
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

const GlobalTopic = "audit:global"

type Logger struct {
	repo *repositories.AuditLogRepository
	bus  *eventbus.Bus
}

// NewLogger builds an audit logger. The bus may be nil (audit still persists).
func NewLogger(repo *repositories.AuditLogRepository, bus *eventbus.Bus) *Logger {
	return &Logger{repo: repo, bus: bus}
}

// Entry describes a single audit event.
type Entry struct {
	ActorID     *uint
	WorkspaceID *uint
	Action      string
	TargetType  string
	TargetID    string
	IP          string
	Metadata    map[string]any
}

// Failures never block the caller: auditing must not break the request path.
func (l *Logger) Record(e Entry) {
	if l == nil || l.repo == nil {
		return
	}
	entry := &models.AuditLog{
		ActorID:     e.ActorID,
		WorkspaceID: e.WorkspaceID,
		Action:      e.Action,
		TargetType:  e.TargetType,
		TargetID:    e.TargetID,
		IPAddress:   e.IP,
		Metadata:    e.Metadata,
	}
	if err := l.repo.Create(entry); err != nil {
		logger.Error("failed to write audit log", "action", e.Action, "error", err)
		return
	}
	if l.bus != nil {
		l.bus.Publish(GlobalTopic, eventbus.Event{Type: e.Action, Data: entry})
	}
}
