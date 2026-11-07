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

package database

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

func statusTopic(instanceID uint) string { return fmt.Sprintf("db-status:%d", instanceID) }

// Per-workspace topic so the Databases list updates live over one SSE connection instead of polling.
func wsStatusTopic(workspaceID uint) string { return fmt.Sprintf("db-status-ws:%d", workspaceID) }

type StatusEvent struct {
	Status  models.DBStatus         `json:"status"`
	Upgrade *models.UpgradeProgress `json:"upgrade,omitempty"`
}

type WorkspaceStatusEvent struct {
	ID      uint                    `json:"id"`
	Status  models.DBStatus         `json:"status"`
	Upgrade *models.UpgradeProgress `json:"upgrade,omitempty"`
}

func (s *Service) publishStatus(inst *models.DatabaseInstance) {
	if s.bus == nil || inst == nil {
		return
	}
	s.bus.Publish(statusTopic(inst.ID), eventbus.Event{
		Type: "status",
		Data: StatusEvent{Status: inst.Status, Upgrade: inst.Upgrade},
	})
	s.bus.Publish(wsStatusTopic(inst.WorkspaceID), eventbus.Event{
		Type: "status",
		Data: WorkspaceStatusEvent{ID: inst.ID, Status: inst.Status, Upgrade: inst.Upgrade},
	})
}

// Not persisted: purely live UX.
func (s *Service) publishProgress(inst *models.DatabaseInstance, message string) {
	if s.bus == nil || inst == nil {
		return
	}
	s.bus.Publish(statusTopic(inst.ID), eventbus.Event{
		Type: "progress",
		Data: map[string]string{"message": message},
	})
}

// With no bus wired it sends the initial snapshot and holds the connection, leaving the client its REST fallback.
func (s *Service) StreamStatus(ctx context.Context, inst *models.DatabaseInstance, send func(eventbus.Event) error) error {
	if err := send(eventbus.Event{Type: "status", Data: StatusEvent{Status: inst.Status, Upgrade: inst.Upgrade}}); err != nil {
		return err
	}
	if s.bus == nil {
		<-ctx.Done()
		return nil
	}
	ch, unsubscribe := s.bus.Subscribe(statusTopic(inst.ID))
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			if err := send(e); err != nil {
				return err
			}
		}
	}
}

// No snapshot is sent: the list seeds via REST, and falls back to periodic reconcile without a bus.
func (s *Service) StreamWorkspaceStatus(ctx context.Context, workspaceID uint, send func(eventbus.Event) error) error {
	if s.bus == nil {
		<-ctx.Done()
		return nil
	}
	ch, unsubscribe := s.bus.Subscribe(wsStatusTopic(workspaceID))
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			if err := send(e); err != nil {
				return err
			}
		}
	}
}
