// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"github.com/miabi-io/miabi/internal/models"
)

// RelocateDeploy deploys the release the app is running on the node its record now names. It reuses the
// active release's image, so a git app is not rebuilt and an image app does not pick up a newer tag: a move
// changes where the app runs, never what runs.
func (s *Service) RelocateDeploy(app *models.Application, trigger string) (*models.Deployment, error) {
	image := ""
	if rel, err := s.releases.FindActive(app.ID); err == nil && rel.Image != "" {
		image = rel.Image
	} else if app.SourceType != models.AppSourceGit {
		image = app.ImageRef("")
	}
	return s.enqueue(app.ID, app.ServerID, image, trigger, app.RegistryID, models.DeployRecreate, false, nil)
}

// ActiveRelease returns the app's live release.
func (s *Service) ActiveRelease(appID uint) (*models.Release, error) {
	return s.releases.FindActive(appID)
}
