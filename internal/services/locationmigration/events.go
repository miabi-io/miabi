// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package locationmigration

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

func topic(id uint) string { return fmt.Sprintf("location-migration:%d", id) }

// publish fans out the migration's current state to the page following it. The row is persisted before
// every publish that matters, so a subscriber that reconnects reads the same state from REST.
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
