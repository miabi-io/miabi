// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformbackup

import (
	"context"
	"fmt"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// AppLifecycle is the slice of the application service needed to quiesce a
// volume: stop the containers using it, and start them again afterwards.
type AppLifecycle interface {
	Stop(ctx context.Context, app *models.Application) error
	Start(ctx context.Context, app *models.Application) (*models.Deployment, error)
}

// MountAppStopper finds the applications mounting a volume and stops them around a restore. Mounts are a
// JSON column rather than a join table, so this scans applications instead of querying by volume — fine
// at the scale involved, and a restore is not a hot path.
type MountAppStopper struct {
	apps      *repositories.ApplicationRepository
	lifecycle AppLifecycle
}

// NewMountAppStopper builds the app-quiesce hook for volume restores.
func NewMountAppStopper(apps *repositories.ApplicationRepository, lifecycle AppLifecycle) *MountAppStopper {
	return &MountAppStopper{apps: apps, lifecycle: lifecycle}
}

// StopUsers stops every running application mounting the volume and returns their ids. Only RUNNING apps
// are returned, so starting them again restores the state that was there before — an app the operator
// had deliberately stopped must not come back up because a volume it mounts was restored.
func (s *MountAppStopper) StopUsers(ctx context.Context, volumeName string) ([]uint, error) {
	if volumeName == "" {
		return nil, nil
	}
	apps, err := s.apps.All()
	if err != nil {
		return nil, err
	}
	var stopped []uint
	for i := range apps {
		app := &apps[i]
		if !mountsVolume(app, volumeName) || app.Status != models.AppStatusRunning {
			continue
		}
		if err := s.lifecycle.Stop(ctx, app); err != nil {
			// Undo what was already stopped: a half-quiesced workspace is worse than
			// a restore that did not start.
			s.startAll(ctx, stopped)
			return nil, fmt.Errorf("stop %s: %w", app.Name, err)
		}
		stopped = append(stopped, app.ID)
	}
	return stopped, nil
}

// StartApps starts the applications StopUsers stopped.
func (s *MountAppStopper) StartApps(ctx context.Context, appIDs []uint) error {
	s.startAll(ctx, appIDs)
	return nil
}

// startAll starts each app, reporting failures rather than stopping at the
// first: every remaining app still needs to come back.
func (s *MountAppStopper) startAll(ctx context.Context, appIDs []uint) {
	for _, id := range appIDs {
		app, err := s.apps.FindByID(id)
		if err != nil {
			logger.Error("restore: could not load an application to start it again", "app", id, "error", err)
			continue
		}
		if _, err := s.lifecycle.Start(ctx, app); err != nil {
			logger.Error("restore: could not start an application again", "app", app.Name, "error", err)
		}
	}
}

func mountsVolume(app *models.Application, volumeName string) bool {
	for _, m := range app.Mounts {
		if m.DockerName == volumeName {
			return true
		}
	}
	return false
}
