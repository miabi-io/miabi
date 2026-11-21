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

package locationmigration

import (
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// statusAtCall captures the app's stored status when each Docker call runs.
type statusAtCall struct {
	docker.Client
	status func() models.AppStatus
	calls  map[string]models.AppStatus
}

func (e *statusAtCall) StopContainer(_ context.Context, _ string, _ int) error {
	e.calls["stop"] = e.status()
	return nil
}

func (e *statusAtCall) RemoveContainer(_ context.Context, _ string, _ bool) error {
	e.calls["remove"] = e.status()
	return nil
}

type oneNode struct{ dc docker.Client }

func (n oneNode) For(uint) (docker.Client, error) { return n.dc, nil }
func (n oneNode) LocalID() uint                   { return 1 }

func TestRemoveRuntimeRecordsIntentFirst(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE applications (id INTEGER PRIMARY KEY, name TEXT, status TEXT, runtime_kind TEXT, updated_at DATETIME, deleted_at DATETIME)`)
	db.Exec(`CREATE TABLE releases (id INTEGER PRIMARY KEY, application_id INTEGER, container_id TEXT, active BOOLEAN)`)
	db.Exec(`INSERT INTO applications (id, name, status, runtime_kind) VALUES (1, 'web', 'running', 'container')`)
	db.Exec(`INSERT INTO releases (id, application_id, container_id, active) VALUES (1, 1, 'abc', true)`)

	apps := repositories.NewApplicationRepository(db)
	engine := &statusAtCall{calls: map[string]models.AppStatus{}, status: func() models.AppStatus {
		var st string
		db.Raw(`SELECT status FROM applications WHERE id = 1`).Scan(&st)
		return models.AppStatus(st)
	}}
	s := &Service{Deps: Deps{
		Apps:    apps,
		App:     application.NewService(apps, nil, repositories.NewReleaseRepository(db), nil, nil, nil, nil, nil, nil, nil, nil, nil),
		Clients: oneNode{engine},
	}}
	app := &models.Application{ID: 1, Name: "web", Status: models.AppStatusRunning, RuntimeKind: models.RuntimeContainer}

	s.removeRuntime(context.Background(), app, 1, 1, true)
	for _, call := range []string{"stop", "remove"} {
		got, ok := engine.calls[call]
		if !ok {
			t.Fatalf("%s was not called", call)
		}
		if got != models.AppStatusStopped {
			t.Errorf("status during %s = %q, want stopped: the event would read as a removal outside Miabi", call, got)
		}
	}
}
