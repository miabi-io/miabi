// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events records and serves application timeline events: lifecycle
// transitions, runtime container events, and configuration changes. Events are
// persisted and fanned out live over the in-process event bus.
package events

import (
	"fmt"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// retainPerApp bounds the event history kept per application.
const retainPerApp = 500

// Topic is the event-bus topic carrying an application's live events.
func Topic(appID uint) string { return fmt.Sprintf("app-events:%d", appID) }

// WorkspaceTopic carries every application event in a workspace, so one
// subscriber (the dashboard) can watch activity and health across all apps
// without subscribing per app. Mirrors the per-workspace database-status fan-out.
func WorkspaceTopic(workspaceID uint) string { return fmt.Sprintf("app-events-ws:%d", workspaceID) }

// Recorder records application events. Implemented by Service; defined as an
// interface so producers (deploy worker, services, docker subscriber) depend on
// the capability, not the package internals.
type Recorder interface {
	Record(e *models.AppEvent)
}

// Notifier reacts to a persisted notifiable event by triggering outbound
// delivery (webhooks, notification channels). Defined here so the recorder
// depends only on the capability; the concrete dispatcher lives in the notify
// package and is injected post-construction via SetNotifier.
type Notifier interface {
	OnEvent(e *models.AppEvent)
}

type Service struct {
	repo      *repositories.AppEventRepository
	bus       *eventbus.Bus
	notifier  Notifier
	alertSink Notifier
}

func NewService(repo *repositories.AppEventRepository, bus *eventbus.Bus) *Service {
	return &Service{repo: repo, bus: bus}
}

// SetNotifier wires the outbound-notification dispatcher. Optional: when unset,
// events are still recorded and streamed, just not delivered externally.
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// SetAlertSink wires the alert engine. Unlike the outbound notifier (gated to the
// curated notifiable set for webhooks/channels), the sink receives EVERY recorded
// event — the engine needs the full signal stream (e.g. health transitions) to
// fire and auto-resolve alerts. Best-effort; never blocks or fails a caller.
func (s *Service) SetAlertSink(n Notifier) { s.alertSink = n }

// Record persists an event and publishes it live. Best-effort: a failure never
// propagates to the caller (recording must not break deploys or mutations).
func (s *Service) Record(e *models.AppEvent) {
	if e == nil || e.ApplicationID == 0 {
		return
	}
	if e.Severity == "" {
		e.Severity = models.SeverityInfo
	}
	if err := s.repo.Create(e); err != nil {
		logger.Error("failed to record app event", "app", e.ApplicationID, "type", e.Type, "error", err)
		return
	}
	if s.bus != nil {
		s.bus.Publish(Topic(e.ApplicationID), eventbus.Event{Type: "event", Data: e})
		// Fan out to the workspace-wide topic so the dashboard's live feed sees
		// events across every app without a per-app subscription.
		if e.WorkspaceID != 0 {
			s.bus.Publish(WorkspaceTopic(e.WorkspaceID), eventbus.Event{Type: "event", Data: e})
		}
	}
	// Fan out to outbound notifications (webhooks, channels). Best-effort and
	// only for the curated notifiable set; must never block or fail the caller.
	if s.notifier != nil && models.IsNotifiable(e.Type) {
		s.notifier.OnEvent(e)
	}
	// Feed the alert engine every event (it filters internally) so it can fire on
	// and auto-resolve from the full signal stream, including health transitions.
	if s.alertSink != nil {
		s.alertSink.OnEvent(e)
	}
	// Opportunistic retention trim (cheap; ignores errors).
	_ = s.repo.TrimByApp(e.ApplicationID, retainPerApp)
}

// Emit is a convenience constructor + Record.
func (s *Service) Emit(workspaceID, appID uint, t models.AppEventType, sev models.AppEventSeverity, message string, meta map[string]string, actorID *uint) {
	s.Record(&models.AppEvent{
		WorkspaceID:   workspaceID,
		ApplicationID: appID,
		Type:          t,
		Severity:      sev,
		Message:       message,
		Metadata:      meta,
		ActorID:       actorID,
	})
}

// List returns an application's events newest-first (cursor via before).
func (s *Service) List(appID uint, limit int, before uint) ([]models.AppEvent, error) {
	return s.repo.ListByApp(appID, limit, before)
}

// ListByWorkspace returns a workspace's application events with offset
// pagination and the total count, ordered by order ("asc"/"desc") and
// optionally filtered to a single severity.
func (s *Service) ListByWorkspace(workspaceID uint, order, severity string, limit, offset int) ([]models.AppEvent, int64, error) {
	return s.repo.ListByWorkspacePaged(workspaceID, order, severity, limit, offset)
}
