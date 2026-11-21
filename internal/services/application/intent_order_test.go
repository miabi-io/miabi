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
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// recordingEngine captures the app's stored status when each Docker call runs.
type recordingEngine struct {
	docker.Client
	status func() models.AppStatus
	calls  map[string]models.AppStatus
}

func (e *recordingEngine) InspectContainer(context.Context, string) (docker.Container, error) {
	return docker.Container{State: "exited"}, nil
}

func (e *recordingEngine) StopContainer(context.Context, string, int) error {
	e.calls["stop"] = e.status()
	return nil
}

func (e *recordingEngine) RemoveContainer(context.Context, string, bool) error {
	e.calls["remove"] = e.status()
	return nil
}

type singleNode struct{ dc docker.Client }

func (n singleNode) For(uint) (docker.Client, error) { return n.dc, nil }
func (n singleNode) LocalID() uint                   { return 1 }
func (n singleNode) ForServiceTask(context.Context, uint, string) (docker.Client, string, error) {
	return n.dc, "", nil
}

func intentService(t *testing.T) (*Service, *gorm.DB, *recordingEngine) {
	t.Helper()
	s, db := managedEnvService(t)
	for _, col := range []string{"status TEXT", "runtime_kind TEXT DEFAULT 'container'", "server_id INTEGER"} {
		db.Exec(`ALTER TABLE applications ADD COLUMN ` + col)
	}
	for _, table := range []string{"deployments", "app_events", "metric_samples", "jobs", "cron_jobs", "job_definitions", "version_policies"} {
		db.Exec(`CREATE TABLE ` + table + ` (id INTEGER PRIMARY KEY, application_id INTEGER, deleted_at DATETIME)`)
	}
	db.Exec(`CREATE TABLE version_allocations (id INTEGER PRIMARY KEY, policy_id INTEGER)`)
	db.Exec(`CREATE TABLE releases (id INTEGER PRIMARY KEY, application_id INTEGER, container_id TEXT, active BOOLEAN)`)
	db.Exec(`INSERT INTO applications (id, workspace_id, name, current_release_id, status) VALUES (1, 1, 'web', 1, 'running')`)
	db.Exec(`INSERT INTO releases (id, application_id, container_id, active) VALUES (1, 1, 'abc', true)`)
	engine := &recordingEngine{calls: map[string]models.AppStatus{}, status: func() models.AppStatus {
		var st string
		db.Raw(`SELECT status FROM applications WHERE id = 1`).Scan(&st)
		return models.AppStatus(st)
	}}
	s.releases = repositories.NewReleaseRepository(db)
	s.clients = singleNode{engine}
	return s, db, engine
}

func findApp(t *testing.T, s *Service) *models.Application {
	t.Helper()
	app, err := s.apps.FindByID(1)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// A destroy or die event that arrives while the app still reads running is taken for a removal outside Miabi.
func TestIntentIsRecordedBeforeDockerCalls(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		s, _, engine := intentService(t)
		app := findApp(t, s)
		if err := s.Stop(context.Background(), app); err != nil {
			t.Fatal(err)
		}
		if got := engine.calls["stop"]; got != models.AppStatusStopped {
			t.Errorf("status during stop = %q, want stopped", got)
		}
	})
	t.Run("delete", func(t *testing.T) {
		s, _, engine := intentService(t)
		app := findApp(t, s)
		if err := s.Delete(context.Background(), app); err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"stop", "remove"} {
			if got := engine.calls[call]; got != models.AppStatusStopped {
				t.Errorf("status during %s = %q, want stopped", call, got)
			}
		}
	})
}
