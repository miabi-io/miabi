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
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestReleaseVersionMatchesTheContainer(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE releases (id INTEGER PRIMARY KEY, application_id INTEGER, version INTEGER, active BOOLEAN)`)
	db.Exec(`INSERT INTO releases (id, application_id, version, active) VALUES (1, 1, 16, true), (2, 1, 17, false)`)
	h := &DeployHandler{releases: repositories.NewReleaseRepository(db)}
	app := &models.Application{ID: 1}
	dep := &models.Deployment{}

	run := h.deployRun(t.Context(), app, dep, "")
	if run.release != 18 {
		t.Fatalf("container release = %d, want 18", run.release)
	}
	db.Exec(`DELETE FROM releases WHERE id = 2`)
	if got := h.releaseVersion(app.ID, dep); got != run.release {
		t.Fatalf("release row = %d, container = %d", got, run.release)
	}
	if got := h.releaseVersion(app.ID, &models.Deployment{}); got != 17 {
		t.Fatalf("without a prediction = %d, want 17", got)
	}
}
