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
	"testing"

	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func managedEnvService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE applications (id INTEGER PRIMARY KEY, workspace_id INTEGER, name TEXT, current_release_id INTEGER,
			redeploy_required BOOLEAN DEFAULT false, miabi_env_disabled BOOLEAN DEFAULT false, stack_id INTEGER, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE app_env_vars (id INTEGER PRIMARY KEY, application_id INTEGER, key TEXT, value TEXT, is_secret BOOLEAN)`,
		`CREATE TABLE app_ports (id INTEGER PRIMARY KEY, application_id INTEGER)`,
		`CREATE TABLE networks (id INTEGER PRIMARY KEY, deleted_at DATETIME)`,
		`CREATE TABLE application_networks (application_id INTEGER, network_id INTEGER)`,
		`CREATE TABLE stacks (id INTEGER PRIMARY KEY, deleted_at DATETIME)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	return &Service{apps: repositories.NewApplicationRepository(db)}, db
}

func redeployRequired(t *testing.T, db *gorm.DB, id uint) bool {
	t.Helper()
	var flag bool
	db.Raw("SELECT redeploy_required FROM applications WHERE id = ?", id).Scan(&flag)
	return flag
}

func TestAddressChangeMarksOnlyReferencingApps(t *testing.T) {
	s, db := managedEnvService(t)
	db.Exec(`INSERT INTO applications (id, workspace_id, name, current_release_id) VALUES
		(1, 1, 'web', 10), (2, 1, 'client', 11), (3, 1, 'other', 12)`)
	db.Exec(`INSERT INTO app_env_vars (id, application_id, key, value, is_secret) VALUES
		(1, 2, 'API', '${{ applications.web.public_url }}', false)`)

	app, err := s.apps.FindByID(1)
	if err != nil {
		t.Fatal(err)
	}
	s.MarkAddressConsumers(app)
	if redeployRequired(t, db, 1) {
		t.Error("attaching a route must not require redeploying the app itself")
	}
	if !redeployRequired(t, db, 2) {
		t.Error("an app that reads the address must be marked")
	}
	if redeployRequired(t, db, 3) {
		t.Error("an unrelated app must not be marked")
	}
}

func TestMarkWorkspaceRenamed(t *testing.T) {
	s, db := managedEnvService(t)
	db.Exec(`INSERT INTO applications (id, workspace_id, name, current_release_id, miabi_env_disabled) VALUES
		(1, 1, 'web', 10, false), (2, 1, 'off', 11, true), (3, 2, 'other', 12, false)`)

	s.MarkWorkspaceRenamed(1)
	if !redeployRequired(t, db, 1) || redeployRequired(t, db, 2) || redeployRequired(t, db, 3) {
		t.Fatalf("marks = %v %v %v", redeployRequired(t, db, 1), redeployRequired(t, db, 2), redeployRequired(t, db, 3))
	}
}
