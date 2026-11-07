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

package worker

import (
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Fails soft: a deleted resource or nil repository leaves the fields empty and renderers fall back to "#id".
func enrichEvent(e *models.AppEvent, apps *repositories.ApplicationRepository, dbs *repositories.DatabaseRepository) {
	if e == nil {
		return
	}
	switch subject, id := e.Subject(); subject {
	case models.SubjectDatabase:
		if dbs == nil || id == 0 {
			return
		}
		inst, err := dbs.FindByID(id)
		if err != nil || inst == nil {
			return
		}
		e.DatabaseName = inst.Name
	default:
		if apps == nil || id == 0 {
			return
		}
		app, err := apps.FindByID(id)
		if err != nil || app == nil {
			return
		}
		e.ApplicationName = app.DisplayName
		e.ApplicationSlug = app.Name
	}
}
