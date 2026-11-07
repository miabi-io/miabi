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
	"github.com/miabi-io/miabi/internal/models"
)

// RelocateDeploy reuses the active image: a move changes where the app runs, never what runs.
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
