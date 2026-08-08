// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// enrichEvent populates the non-persisted display fields on an event so downstream renderers can
// name the application instead of showing a bare id. It fails soft: a missing app or nil repository
// leaves the fields empty and renderers fall back to "#id".
func enrichEvent(e *models.AppEvent, apps *repositories.ApplicationRepository) {
	if e == nil || apps == nil || e.ApplicationID == 0 {
		return
	}
	app, err := apps.FindByID(e.ApplicationID)
	if err != nil || app == nil {
		return
	}
	e.ApplicationName = app.DisplayName
	e.ApplicationSlug = app.Name
}
