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

// Subscriber maps Docker container events to AppEvents for apps and databases; run one per process.
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
	// Container events stream unfiltered, so ignore anything without the app or database label.
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
	// Load the app first: an intentional stop may end in a non-graceful exit code.
	app, err := s.apps.FindByID(appID)
	if err != nil {
		return // app deleted; ignore
	}
	if ev.Action == "destroy" && !s.activeContainerRemoved(ev, app) {
		return
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

// Doesn't write instance status: the database service owns it and writing here would race its upgrade state machine.
func (s *Subscriber) handleDatabase(ev docker.EngineEvent, databaseID uint) {
	if s.databases == nil || ev.Action == "destroy" {
		return
	}
	inst, err := s.databases.FindByID(databaseID)
	if err != nil {
		return
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

// userStopped makes a non-graceful exit of an intentionally stopped resource read as a stop.
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
	case ev.Action == "destroy":
		typ, sev, msg = models.EventContainerRemoved, models.SeverityWarning, "Container removed"
	default:
		return "", "", "", false
	}
	return typ, sev, msg, true
}

// Acts only on the active release container so retired old or canary containers never flip status.
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

// Deploys and stops remove containers routinely; other removals of the active container are what make it missing.
func (s *Subscriber) activeContainerRemoved(ev docker.EngineEvent, app *models.Application) bool {
	if s.releases == nil || app.Status == models.AppStatusDeploying || app.Status == models.AppStatusStopped {
		return false
	}
	rel, err := s.releases.FindActive(app.ID)
	return err == nil && rel.ContainerID != "" && rel.ContainerID == ev.ContainerID
}

// Any exit of a user-stopped resource is a stop, since a shell-form CMD swallows SIGTERM and ends in 137.
func dieIsStop(exitCode string, userStopped bool) bool {
	return exitCode == "0" || exitCode == "143" || userStopped
}

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
