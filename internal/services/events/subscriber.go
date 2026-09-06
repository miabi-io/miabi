// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Subscriber translates Docker daemon container events into AppEvents for both application
// and database-instance containers. Run a single instance per process connected to the Docker
// daemon.
type Subscriber struct {
	docker    docker.Client
	apps      *repositories.ApplicationRepository
	releases  *repositories.ReleaseRepository
	databases *repositories.DatabaseRepository
	rec       Recorder
}

func NewSubscriber(d docker.Client, apps *repositories.ApplicationRepository, releases *repositories.ReleaseRepository, databases *repositories.DatabaseRepository, rec Recorder) *Subscriber {
	return &Subscriber{docker: d, apps: apps, releases: releases, databases: databases, rec: rec}
}

// Run streams engine events until ctx is cancelled, reconnecting on error.
func (s *Subscriber) Run(ctx context.Context) {
	logger.Info("container event subscriber started")
	for {
		err := s.docker.StreamEvents(ctx, func(ev docker.EngineEvent) error {
			s.handle(ev)
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		logger.Warn("docker event stream ended; reconnecting", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (s *Subscriber) handle(ev docker.EngineEvent) {
	// Container events stream unfiltered, so ignore anything that isn't one of ours —
	// i.e. carries neither the io.miabi.app nor the io.miabi.database label.
	if id, ok := labelID(ev.Attributes, docker.LabelApp); ok {
		s.handleApp(ev, id)
		return
	}
	if id, ok := labelID(ev.Attributes, docker.LabelDatabase); ok {
		s.handleDatabase(ev, id)
	}
}

func labelID(attrs map[string]string, label string) (uint, bool) {
	v, ok := docker.LabelValue(attrs, label)
	if !ok || v == "" {
		return 0, false
	}
	id, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(id), true
}

func (s *Subscriber) handleApp(ev docker.EngineEvent, appID uint) {
	// Load the app first so the die-event classification can honor an in-progress
	// stop (an intentional stop may end in a non-graceful exit code).
	app, err := s.apps.FindByID(appID)
	if err != nil {
		return // app deleted; ignore
	}
	typ, sev, msg, ok := classify(ev, app.Status == models.AppStatusStopped)
	if !ok {
		return
	}
	s.rec.Record(&models.AppEvent{
		WorkspaceID:   app.WorkspaceID,
		SubjectType:   models.SubjectApp,
		ApplicationID: appID,
		Type:          typ,
		Severity:      sev,
		Message:       msg,
		Metadata:      map[string]string{"container_id": ev.ContainerID},
	})

	s.reconcileStatus(ev, app)
}

// handleDatabase records runtime events for a managed database instance. Unlike the app path it
// does not write back an instance status: the database service owns that field (it drives the
// upgrade state machine) and the detail page reads live status from Docker anyway, so writing it
// here would race the service for no gain.
func (s *Subscriber) handleDatabase(ev docker.EngineEvent, databaseID uint) {
	if s.databases == nil {
		return
	}
	inst, err := s.databases.FindByID(databaseID)
	if err != nil {
		return // instance deleted; ignore
	}
	typ, sev, msg, ok := classify(ev, inst.Status == models.DBStatusStopped)
	if !ok {
		return
	}
	s.rec.Record(&models.AppEvent{
		WorkspaceID: inst.WorkspaceID,
		SubjectType: models.SubjectDatabase,
		DatabaseID:  databaseID,
		Type:        typ,
		Severity:    sev,
		Message:     msg,
		Metadata:    map[string]string{"container_id": ev.ContainerID, "engine": string(inst.Engine)},
	})
}

// classify maps a Docker container action to a timeline event. userStopped tells the die
// classification that the resource was stopped on purpose, so a non-graceful exit code still
// reads as a stop. Pure, so it is unit-testable; ok is false for actions that produce no event.
func classify(ev docker.EngineEvent, userStopped bool) (typ models.AppEventType, sev models.AppEventSeverity, msg string, ok bool) {
	sev = models.SeverityInfo
	switch {
	case ev.Action == "start":
		typ, msg = models.EventContainerStarted, "Container started"
	case ev.Action == "oom":
		typ, sev, msg = models.EventContainerOOM, models.SeverityError, "Container ran out of memory"
	case strings.HasPrefix(ev.Action, "health_status"):
		typ = models.EventContainerHealth
		if strings.Contains(ev.Action, "unhealthy") {
			sev, msg = models.SeverityWarning, "Container is unhealthy"
		} else {
			msg = "Container is healthy"
		}
	case ev.Action == "die":
		code := ev.Attributes["exitCode"]
		if dieIsStop(code, userStopped) {
			typ, msg = models.EventContainerStopped, "Container stopped"
		} else {
			typ, sev, msg = models.EventContainerDied, models.SeverityError, "Container exited (code "+code+")"
		}
	default:
		return "", "", "", false
	}
	return typ, sev, msg, true
}

// reconcileStatus keeps the stored app.Status in sync with the live container.
// It only acts on the app's active release container (so retired old/canary
// containers never flip status), and on unexpected exits/recoveries.
func (s *Subscriber) reconcileStatus(ev docker.EngineEvent, app *models.Application) {
	if s.releases == nil {
		return
	}
	rel, err := s.releases.FindActive(app.ID)
	if err != nil || rel.ContainerID == "" || rel.ContainerID != ev.ContainerID {
		return
	}
	if next, change := nextStoredStatus(ev.Action, ev.Attributes["exitCode"], app.Status); change {
		_ = s.apps.SetStatus(app.ID, next)
	}
}

// dieIsStop reports whether a container "die" with the given exit code is a clean stop rather than
// a crash. Exit 0 and 143 are always stops; any exit of a resource the user already stopped is too,
// since a shell-form CMD swallows SIGTERM and ends in 137. Pure, so it is unit-testable.
func dieIsStop(exitCode string, userStopped bool) bool {
	return exitCode == "0" || exitCode == "143" || userStopped
}

// nextStoredStatus decides how a Docker lifecycle event should update the stored
// app status. Pure (no DB) so it can be unit-tested. Returns the new status and
// whether a change is warranted.
func nextStoredStatus(action, exitCode string, current models.AppStatus) (models.AppStatus, bool) {
	switch action {
	case "oom":
		if current == models.AppStatusRunning {
			return models.AppStatusFailed, true
		}
	case "die":
		// Graceful exits (0) and SIGTERM (143) are stops/retires — not crashes.
		if exitCode != "0" && exitCode != "143" && current == models.AppStatusRunning {
			return models.AppStatusFailed, true
		}
	case "start":
		if current == models.AppStatusFailed {
			return models.AppStatusRunning, true
		}
	}
	return current, false
}
