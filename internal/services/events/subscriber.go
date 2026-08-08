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

// Subscriber translates Docker daemon container events into AppEvents. Run a
// single instance per process connected to the Docker daemon.
type Subscriber struct {
	docker   docker.Client
	apps     *repositories.ApplicationRepository
	releases *repositories.ReleaseRepository
	rec      Recorder
}

func NewSubscriber(d docker.Client, apps *repositories.ApplicationRepository, releases *repositories.ReleaseRepository, rec Recorder) *Subscriber {
	return &Subscriber{docker: d, apps: apps, releases: releases, rec: rec}
}

// Run streams engine events until ctx is cancelled, reconnecting on error.
func (s *Subscriber) Run(ctx context.Context) {
	logger.Info("application event subscriber started")
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
	// Container events stream unfiltered, so ignore anything that isn't one of
	// ours — i.e. carries no io.miabi.app label.
	appIDStr, ok := docker.LabelValue(ev.Attributes, docker.LabelApp)
	if !ok || appIDStr == "" {
		return
	}
	appID64, err := strconv.ParseUint(appIDStr, 10, 64)
	if err != nil {
		return
	}
	appID := uint(appID64)

	// Load the app first so the die-event classification can honor an in-progress
	// stop (an intentional stop may end in a non-graceful exit code).
	app, err := s.apps.FindByID(appID)
	if err != nil {
		return // app deleted; ignore
	}

	var (
		typ models.AppEventType
		sev = models.SeverityInfo
		msg string
	)
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
		if dieIsStop(code, app.Status) {
			typ, msg = models.EventContainerStopped, "Container stopped"
		} else {
			typ, sev, msg = models.EventContainerDied, models.SeverityError, "Container exited (code "+code+")"
		}
	default:
		return
	}
	s.rec.Record(&models.AppEvent{
		WorkspaceID:   app.WorkspaceID,
		ApplicationID: appID,
		Type:          typ,
		Severity:      sev,
		Message:       msg,
		Metadata:      map[string]string{"container_id": ev.ContainerID},
	})

	s.reconcileStatus(ev, app)
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

// dieIsStop reports whether a container "die" with the given exit code, for an app in the given stored status,
// is a clean stop rather than a crash. Exit 0 and 143 are always stops; any exit of an app the user already
// stopped is too, since a shell-form CMD swallows SIGTERM and ends in 137. Pure, so it is unit-testable.
func dieIsStop(exitCode string, status models.AppStatus) bool {
	return exitCode == "0" || exitCode == "143" || status == models.AppStatusStopped
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
