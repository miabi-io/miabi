// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events records and serves resource timeline events: lifecycle
// transitions, runtime container events, and configuration changes. Events are
// persisted and fanned out live over the in-process event bus.
//
// A single recorder serves every subject (applications, database instances), so a new
// subject inherits alerting, outbound notifications and the workspace feed for free.
package events

import (
	"fmt"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// retainPerSubject bounds the event history kept per subject (application or database).
const retainPerSubject = 500

// Topic is the event-bus topic carrying an application's live events.
func Topic(appID uint) string { return fmt.Sprintf("app-events:%d", appID) }

// DatabaseTopic is the event-bus topic carrying a database instance's live events.
func DatabaseTopic(databaseID uint) string { return fmt.Sprintf("db-events:%d", databaseID) }

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

// Notifier reacts to a persisted notifiable event by triggering outbound delivery (webhooks, notification
// channels). Defined here so the recorder depends only on the capability; the concrete dispatcher lives
// in the notify package and is injected post-construction via SetNotifier.
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

// SetAlertSink wires the alert engine. Unlike the outbound notifier, which is gated to the curated
// notifiable set, the sink receives EVERY recorded event — the engine needs the full signal stream, such
// as health transitions, to fire and auto-resolve alerts. Best-effort; never blocks or fails a caller.
func (s *Service) SetAlertSink(n Notifier) { s.alertSink = n }

// Record persists an event and publishes it live. Best-effort: a failure never
// propagates to the caller (recording must not break deploys or mutations).
func (s *Service) Record(e *models.AppEvent) {
	if e == nil {
		return
	}
	if e.SubjectType == "" {
		e.SubjectType = models.SubjectApp
	}
	if !e.HasSubject() {
		return
	}
	if e.Severity == "" {
		e.Severity = models.SeverityInfo
	}
	subject, subjectID := e.Subject()
	if err := s.repo.Create(e); err != nil {
		logger.Error("failed to record event", "subject", subject, "id", subjectID, "type", e.Type, "error", err)
		return
	}
	if s.bus != nil {
		s.bus.Publish(subjectTopic(subject, subjectID), eventbus.Event{Type: "event", Data: e})
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
	if subject == models.SubjectDatabase {
		_ = s.repo.TrimByDatabase(subjectID, retainPerSubject)
	} else {
		_ = s.repo.TrimByApp(subjectID, retainPerSubject)
	}
}

func subjectTopic(subject models.EventSubjectType, id uint) string {
	if subject == models.SubjectDatabase {
		return DatabaseTopic(id)
	}
	return Topic(id)
}

// Emit is a convenience constructor + Record.
func (s *Service) Emit(workspaceID, appID uint, t models.AppEventType, sev models.AppEventSeverity, message string, meta map[string]string, actorID *uint) {
	s.Record(&models.AppEvent{
		WorkspaceID:   workspaceID,
		SubjectType:   models.SubjectApp,
		ApplicationID: appID,
		Type:          t,
		Severity:      sev,
		Message:       message,
		Metadata:      meta,
		ActorID:       actorID,
	})
}

// EmitDatabase records an event about a database instance. The instance is the subject even
// for backup and restore events, whose logical database name goes in meta, so one timeline
// covers the whole instance.
//
// name is not persisted, but it rides along on the live bus event so SSE consumers can label
// the subject without a lookup they have no data for.
func (s *Service) EmitDatabase(workspaceID, databaseID uint, name string, t models.AppEventType, sev models.AppEventSeverity, message string, meta map[string]string, actorID *uint) {
	s.Record(&models.AppEvent{
		WorkspaceID:  workspaceID,
		SubjectType:  models.SubjectDatabase,
		DatabaseID:   databaseID,
		DatabaseName: name,
		Type:         t,
		Severity:     sev,
		Message:      message,
		Metadata:     meta,
		ActorID:      actorID,
	})
}

// ListByDatabase returns a database instance's events newest-first (cursor via before).
func (s *Service) ListByDatabase(databaseID uint, limit int, before uint) ([]models.AppEvent, error) {
	return s.repo.ListByDatabase(databaseID, limit, before)
}

// DeleteByDatabase drops a deleted instance's timeline.
func (s *Service) DeleteByDatabase(databaseID uint) error {
	return s.repo.DeleteByDatabase(databaseID)
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
