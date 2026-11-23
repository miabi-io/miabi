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

package job

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

var (
	ErrPreReleaseInvalid = errors.New("invalid pre-release command")
	// ErrPreReleaseNameTaken means a declared Job already uses the name the console command is stored under.
	ErrPreReleaseNameTaken = errors.New("a job definition already uses this app's pre-release name")
)

// maxPreReleaseBackoff mirrors the manifest's backoffLimit cap.
const maxPreReleaseBackoff = 10

type PreReleaseCommand struct {
	Command      []string `json:"command"`
	TimeoutSecs  int      `json:"timeout_secs"`
	BackoffLimit int      `json:"backoff_limit"`
}

func PreReleaseName(appName string) string { return appName + "-pre-release" }

func (s *Service) App(workspaceID, appID uint) (*models.Application, error) {
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, ErrNotFound
	}
	return app, nil
}

// ListForDeployment returns the beforeRelease runs that gated a deployment, oldest first.
func (s *Service) ListForDeployment(workspaceID, deploymentID uint) ([]models.Job, error) {
	jobs, err := s.repo.ListByDeployment(workspaceID, deploymentID)
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		jobs[i].Node = s.pinName(jobs[i].ServerID)
	}
	return jobs, nil
}

// GetPreReleaseCommand returns nil when the app has no console pre-release command.
func (s *Service) GetPreReleaseCommand(workspaceID uint, app *models.Application) (*models.JobDefinition, error) {
	def, err := s.repo.FindJobDefinitionByName(workspaceID, PreReleaseName(app.Name))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !ownedByApp(def, app) {
		return nil, nil
	}
	def.AppName = app.Name
	def.LastStatus = s.LastRunStatus(def)
	return def, nil
}

// SetPreReleaseCommand saves without running: the next deploy runs it.
func (s *Service) SetPreReleaseCommand(ctx context.Context, workspaceID uint, app *models.Application, in PreReleaseCommand) (*models.JobDefinition, error) {
	if err := validPreRelease(in); err != nil {
		return nil, err
	}
	name := PreReleaseName(app.Name)
	cur, err := s.repo.FindJobDefinitionByName(workspaceID, name)
	switch {
	case err == nil && !ownedByApp(cur, app):
		return nil, fmt.Errorf("%w: %q", ErrPreReleaseNameTaken, name)
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}
	meta := models.SetOwner(models.Metadata{models.MetaManagedBy: models.ManagedByUser}, models.OwnerApp, app.ID, app.Name)
	def, _, err := s.SaveDefinition(ctx, workspaceID, DefinitionInput{
		Name: name, ApplicationID: app.ID, Command: in.Command,
		TimeoutSecs: in.TimeoutSecs, BackoffLimit: in.BackoffLimit,
		RunPolicy: models.JobRunBeforeRelease, Metadata: meta,
	}, false)
	return def, err
}

func (s *Service) DeletePreReleaseCommand(ctx context.Context, workspaceID uint, app *models.Application) error {
	def, err := s.GetPreReleaseCommand(workspaceID, app)
	if err != nil || def == nil {
		return err
	}
	return s.DeleteDefinition(ctx, workspaceID, def.ID)
}

func ownedByApp(def *models.JobDefinition, app *models.Application) bool {
	owner, ok := models.Owner(def.Metadata)
	return ok && owner.Kind == models.OwnerApp && owner.ID == app.ID && def.ApplicationID == app.ID
}

func validPreRelease(in PreReleaseCommand) error {
	if len(in.Command) == 0 {
		return ErrNoCommand
	}
	if in.TimeoutSecs <= 0 {
		return fmt.Errorf("%w: a timeout is required, since the deploy waits for the command", ErrPreReleaseInvalid)
	}
	if in.BackoffLimit < 0 || in.BackoffLimit > maxPreReleaseBackoff {
		return fmt.Errorf("%w: retries must be between 0 and %d", ErrPreReleaseInvalid, maxPreReleaseBackoff)
	}
	if in.TimeoutSecs*(in.BackoffLimit+1) > models.BeforeReleaseBudgetSecs {
		return fmt.Errorf("%w: timeout × (retries + 1) must not exceed %s seconds, the time a deploy can wait",
			ErrPreReleaseInvalid, strconv.Itoa(models.BeforeReleaseBudgetSecs))
	}
	return nil
}
