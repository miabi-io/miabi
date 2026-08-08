// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

func statusTopic(instanceID uint) string { return fmt.Sprintf("db-status:%d", instanceID) }

// wsStatusTopic is the per-workspace topic carrying lifecycle status changes for
// every instance in the workspace, so the Databases list can update rows live
// (one SSE connection) instead of polling.
func wsStatusTopic(workspaceID uint) string { return fmt.Sprintf("db-status-ws:%d", workspaceID) }

// StatusEvent is the snapshot pushed on the "status" stream: the instance's
// lifecycle status and any in-flight upgrade progress. The detail page replaces
// its state from this, so a status/phase change needs no extra request.
type StatusEvent struct {
	Status  models.DBStatus         `json:"status"`
	Upgrade *models.UpgradeProgress `json:"upgrade,omitempty"`
}

// WorkspaceStatusEvent is StatusEvent plus the instance id, so a workspace-wide
// subscriber (the list page) knows which row changed.
type WorkspaceStatusEvent struct {
	ID      uint                    `json:"id"`
	Status  models.DBStatus         `json:"status"`
	Upgrade *models.UpgradeProgress `json:"upgrade,omitempty"`
}

// publishStatus fans out the instance's current lifecycle status to subscribers:
// the per-instance topic (detail page) and the per-workspace topic (list page).
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

// publishProgress fans out a transient, human-readable progress line (e.g. the
// bring-up steps during provisioning). Not persisted — purely for live UX.
func (s *Service) publishProgress(inst *models.DatabaseInstance, message string) {
	if s.bus == nil || inst == nil {
		return
	}
	s.bus.Publish(statusTopic(inst.ID), eventbus.Event{
		Type: "progress",
		Data: map[string]string{"message": message},
	})
}

// StreamStatus sends the instance's current status, then live updates, until the context is cancelled.
// It backs the detail page's SSE. With no bus wired it sends the initial snapshot and holds the
// connection open, so the client still has its REST fallback.
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

// StreamWorkspaceStatus streams lifecycle status changes for every instance in a workspace until the
// context is cancelled, delivering {id,status} deltas to the Databases list. The list seeds its initial
// state via REST, so no snapshot is sent. With no bus wired the client falls back to periodic reconcile.
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
