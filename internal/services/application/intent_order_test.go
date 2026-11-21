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
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// recordingEngine captures the app's stored status when each Docker call runs.
type recordingEngine struct {
	docker.Client
	state  string
	status func() models.AppStatus
	calls  map[string]models.AppStatus
}

func (e *recordingEngine) InspectContainer(context.Context, string) (docker.Container, error) {
	if e.state == "" {
		return docker.Container{State: "exited"}, nil
	}
	return docker.Container{State: e.state}, nil
}

func (e *recordingEngine) StatsOnce(context.Context, string) (docker.StatsSample, error) {
	return docker.StatsSample{}, errors.New("no stats")
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
		s, db, engine := intentService(t)
		app := findApp(t, s)
		if err := s.Stop(context.Background(), app); err != nil {
			t.Fatal(err)
		}
		if got := engine.calls["stop"]; got != models.AppStatusStopping {
			t.Errorf("status during stop = %q, want stopping", got)
		}
		if got := storedStatus(db); got != models.AppStatusStopped {
			t.Errorf("status after stop = %q, want stopped", got)
		}
	})
	t.Run("delete", func(t *testing.T) {
		s, _, engine := intentService(t)
		app := findApp(t, s)
		if err := s.Delete(context.Background(), app); err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"stop", "remove"} {
			if got := engine.calls[call]; got != models.AppStatusDeleting {
				t.Errorf("status during %s = %q, want deleting", call, got)
			}
		}
	})
}

func storedStatus(db *gorm.DB) models.AppStatus {
	var st string
	db.Raw(`SELECT status FROM applications WHERE id = 1`).Scan(&st)
	return models.AppStatus(st)
}

func TestLifecycleRefusedWhileBusy(t *testing.T) {
	cases := []struct {
		status models.AppStatus
		want   error
	}{
		{models.AppStatusDeleting, ErrAppDeleting},
		{models.AppStatusStopping, ErrAppStopping},
	}
	for _, c := range cases {
		t.Run(string(c.status), func(t *testing.T) {
			s, _, engine := intentService(t)
			app := findApp(t, s)
			app.Status = c.status
			if _, err := s.Start(context.Background(), app); !errors.Is(err, c.want) {
				t.Errorf("start = %v, want %v", err, c.want)
			}
			if _, err := s.Restart(context.Background(), app); !errors.Is(err, c.want) {
				t.Errorf("restart = %v, want %v", err, c.want)
			}
			if len(engine.calls) != 0 {
				t.Errorf("Docker was called: %v", engine.calls)
			}
		})
	}
	t.Run("stop", func(t *testing.T) {
		s, _, _ := intentService(t)
		app := findApp(t, s)
		app.Status = models.AppStatusDeleting
		if err := s.Stop(context.Background(), app); !errors.Is(err, ErrAppDeleting) {
			t.Errorf("stop while deleting = %v", err)
		}
		app.Status = models.AppStatusStopping
		if err := s.Stop(context.Background(), app); err != nil {
			t.Errorf("stop while stopping = %v, want a no-op", err)
		}
	})
}

func TestSettleTransitional(t *testing.T) {
	t.Run("stopping with the container exited", func(t *testing.T) {
		s, db, _ := intentService(t)
		db.Exec(`UPDATE applications SET status = 'stopping' WHERE id = 1`)
		if err := s.SettleTransitional(context.Background(), findApp(t, s)); err != nil {
			t.Fatal(err)
		}
		if got := storedStatus(db); got != models.AppStatusStopped {
			t.Errorf("status = %q, want stopped", got)
		}
	})
	t.Run("stopping with the container still running", func(t *testing.T) {
		s, db, engine := intentService(t)
		engine.state = "running"
		db.Exec(`UPDATE applications SET status = 'stopping' WHERE id = 1`)
		if err := s.SettleTransitional(context.Background(), findApp(t, s)); err != nil {
			t.Fatal(err)
		}
		if got := storedStatus(db); got != models.AppStatusRunning {
			t.Errorf("status = %q, want running: the stop never happened", got)
		}
	})
	t.Run("deleting resumes even if the container still runs", func(t *testing.T) {
		s, db, engine := intentService(t)
		engine.state = "running"
		db.Exec(`UPDATE applications SET status = 'deleting' WHERE id = 1`)
		if err := s.SettleTransitional(context.Background(), findApp(t, s)); err != nil {
			t.Fatal(err)
		}
		if _, ok := engine.calls["remove"]; !ok {
			t.Error("the container was not removed")
		}
		var n int64
		db.Raw(`SELECT COUNT(*) FROM applications WHERE id = 1 AND deleted_at IS NULL`).Scan(&n)
		if n != 0 {
			t.Error("the app row survived the resumed delete")
		}
	})
}
