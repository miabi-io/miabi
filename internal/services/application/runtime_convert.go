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

package application

import (
	"errors"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

var (
	// ErrRuntimeChangeBlocked is returned when a preflight found a blocker; RuntimeChangeError lists them.
	ErrRuntimeChangeBlocked = errors.New("the runtime change is blocked")
	// The generic settings update would neither redeploy nor hand over between runtimes.
	ErrRuntimeChangeViaSettings = errors.New("change the runtime with POST /apps/{id}/runtime, which redeploys and hands over between runtimes")
)

// RuntimeChange asks to move an app between the container and service runtimes.
type RuntimeChange struct {
	Runtime models.RuntimeKind
	// Replicas applies to the service runtime; 0 keeps the stored count.
	Replicas int
}

// RuntimePreflight is what a runtime change would do, and what stops it.
type RuntimePreflight struct {
	From     models.RuntimeKind    `json:"from"`
	To       models.RuntimeKind    `json:"to"`
	Replicas int                   `json:"replicas"`
	ServerID uint                  `json:"server_id"`
	Strategy models.DeployStrategy `json:"strategy"`
	// Redeploy is false for an app that was never deployed: the change is only saved.
	Redeploy bool     `json:"redeploy"`
	Blockers []string `json:"blockers"`
	Warnings []string `json:"warnings"`
}

// RuntimeChangeError carries the blockers of a refused change.
type RuntimeChangeError struct{ Blockers []string }

func (e *RuntimeChangeError) Error() string {
	return "the runtime change is blocked: " + strings.Join(e.Blockers, "; ")
}
func (e *RuntimeChangeError) Unwrap() error { return ErrRuntimeChangeBlocked }

// Code is the stable machine code surfaced in the API error envelope.
func (e *RuntimeChangeError) Code() string { return "RUNTIME_CHANGE_BLOCKED" }

// managedElsewhere reports whether a manifest owns the app, so a console change would be undone.
func managedElsewhere(app *models.Application) bool {
	switch app.Metadata[models.MetaManagedBy] {
	case models.ManagedByGitOps, models.ManagedByApply:
		return true
	}
	return false
}

// PreflightRuntime reports what changing app to ch would do without changing anything.
func (s *Service) PreflightRuntime(app *models.Application, ch RuntimeChange) RuntimePreflight {
	p, _ := s.planRuntime(app, ch)
	return p
}

// planRuntime builds the preflight and the app as it would be saved.
func (s *Service) planRuntime(app *models.Application, ch RuntimeChange) (RuntimePreflight, *models.Application) {
	next := *app
	p := RuntimePreflight{From: app.RuntimeKind, To: ch.Runtime, Redeploy: app.CurrentReleaseID != nil,
		Strategy: models.DeployRolling, Blockers: []string{}, Warnings: []string{}}
	block := func(format string, a ...any) { p.Blockers = append(p.Blockers, fmt.Sprintf(format, a...)) }
	warn := func(format string, a ...any) { p.Warnings = append(p.Warnings, fmt.Sprintf(format, a...)) }

	if !models.ValidRuntimeKind(ch.Runtime) {
		block("unknown runtime %q", ch.Runtime)
		return p, nil
	}
	if ch.Runtime == app.RuntimeKind {
		block("the app already runs as a %s", ch.Runtime)
	}
	if managedElsewhere(app) {
		block("the app is managed by a manifest; change its runtime there")
	}
	if app.CanaryReleaseID != nil {
		block("a canary deployment is in progress; promote or abort it first")
	}

	localNode, lerr := s.localVolumeNode(app)
	if lerr != nil {
		block("%v", lerr)
	}
	next.RuntimeKind = ch.Runtime
	switch ch.Runtime {
	case models.RuntimeService:
		next.Replicas = app.Replicas
		if ch.Replicas > 0 {
			next.Replicas = ch.Replicas
		}
		if next.Replicas < 1 {
			next.Replicas = 1
		}
		if app.GPUCount > 0 {
			block("GPU apps must run as a single container")
		}
		if localNode != nil && next.Replicas == 1 {
			warn("a node-local volume keeps the service on its node, so it does not fail over to another node")
		}
		if s.portBindings != nil {
			if b, err := s.portBindings.ListApprovedByApp(app.ID); err == nil && len(b) > 0 {
				warn("published host ports are not bound by a service; reach it through its routes")
			}
		}
	case models.RuntimeContainer:
		next.Replicas = 1
		if app.RuntimeKind == models.RuntimeService && app.Replicas > 1 {
			warn("replicas drop from %d to 1", app.Replicas)
		}
		// The node the app was placed on, unless its node-local volume lives elsewhere.
		if localNode != nil {
			next.ServerID = *localNode
		}
	}
	p.Replicas, p.ServerID = next.Replicas, next.ServerID
	if localNode != nil {
		p.Strategy = models.DeployRecreate
		if p.Redeploy {
			warn("the node-local volume can't be held by both runtimes at once, so the app stops briefly during the switch")
		}
	}

	if err := s.validateRuntime(&next, app.RuntimeKind); err != nil {
		block("%v", err)
	} else if next.RuntimeKind != ch.Runtime {
		block("the service runtime is not available for this app")
	}
	if err := s.checkCompute(&next, next.Replicas); err != nil {
		block("%v", err)
	}
	return p, &next
}

// localVolumeNode is the node holding the app's node-local (rwo) managed volume, nil when it has none.
func (s *Service) localVolumeNode(app *models.Application) (*uint, error) {
	for _, m := range app.Mounts {
		if m.VolumeID == 0 || m.HostPreset != "" {
			continue
		}
		v, err := s.resolveMountVolume(app.WorkspaceID, m.VolumeID)
		if err != nil {
			return nil, err
		}
		if v.AccessMode != models.AccessRWX && v.Driver != models.VolumeDriverHost {
			id := v.ServerID
			return &id, nil
		}
	}
	return nil, nil
}

// ChangeRuntime returns a nil deployment when the app was never deployed.
func (s *Service) ChangeRuntime(app *models.Application, ch RuntimeChange, triggeredBy *uint) (*models.Deployment, error) {
	p, next := s.planRuntime(app, ch)
	if len(p.Blockers) > 0 {
		return nil, &RuntimeChangeError{Blockers: p.Blockers}
	}
	delete(next.Metadata, models.MetaRuntimeAutoService)
	next.Stack, next.Networks, next.Ports = nil, nil, nil
	if err := s.Update(next); err != nil {
		return nil, err
	}
	*app = *next
	msg := "Runtime changed to container"
	if next.RuntimeKind == models.RuntimeService {
		msg = fmt.Sprintf("Runtime changed to service (%d replica(s))", next.Replicas)
	}
	s.emit(app, models.EventSettingsUpdated, msg)
	if !p.Redeploy {
		return nil, nil
	}
	image := ""
	if app.SourceType != models.AppSourceGit {
		image = app.ImageRef("")
	}
	return s.enqueue(app.ID, app.ServerID, image, "runtime", app.RegistryID, p.Strategy, false, triggeredBy)
}
