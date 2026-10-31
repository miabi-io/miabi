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

package main

import (
	"github.com/miabi-io/miabi/internal/services/image"
	"github.com/miabi-io/miabi/internal/services/registryserver"
	"github.com/miabi-io/miabi/internal/services/versioning"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// newVersionService builds the version keeper a pipeline worker uses to check a run's tag before
// dispatch and to give its version back when the run fails before a runner reports.
func newVersionService(db *gorm.DB, registry *registryserver.Service) *versioning.Service {
	svc := versioning.NewService(repositories.NewVersionRepository(db), repositories.NewApplicationRepository(db), repositories.NewPipelineRepository(db))
	svc.SetRegistry(registry)
	svc.SetImages(image.NewService(repositories.NewImageRepository(db), repositories.NewReleaseRepository(db)))
	return svc
}
