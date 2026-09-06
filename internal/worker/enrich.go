// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// enrichEvent populates the non-persisted display fields on an event so downstream renderers can
// name the resource instead of showing a bare id. It fails soft: a deleted resource or nil
// repository leaves the fields empty and renderers fall back to "#id".
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
