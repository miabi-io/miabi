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

package locationmigration

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

func topic(id uint) string { return fmt.Sprintf("location-migration:%d", id) }

// The row is persisted before every publish that matters, so a reconnecting subscriber reads the same state via REST.
func (s *Service) publish(m *models.LocationMigration) {
	if s.Bus == nil || m == nil {
		return
	}
	snap := *m
	snap.Progress.Items = append([]models.MigrationProgressItem(nil), m.Progress.Items...)
	s.Bus.Publish(topic(m.ID), eventbus.Event{Type: "migration", Data: &snap})
}

// Stream sends the migration's state, then every change, until the context ends.
func (s *Service) Stream(ctx context.Context, m *models.LocationMigration, send func(eventbus.Event) error) error {
	if err := send(eventbus.Event{Type: "migration", Data: m}); err != nil {
		return err
	}
	if s.Bus == nil {
		<-ctx.Done()
		return nil
	}
	ch, unsubscribe := s.Bus.Subscribe(topic(m.ID))
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
