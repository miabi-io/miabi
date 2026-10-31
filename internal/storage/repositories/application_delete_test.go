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

package repositories

import (
	"fmt"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Deleting an app removes its CronJobs and Job runs, so no schedule outlives
// the app it would run in.
func TestApplicationDeleteRemovesJobs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("CREATE TABLE applications (id INTEGER PRIMARY KEY, workspace_id INTEGER)")
	db.Exec("CREATE TABLE application_networks (application_id INTEGER, network_id INTEGER)")
	for _, tbl := range []string{"app_env_vars", "app_ports", "deployments", "releases", "app_events", "metric_samples", "jobs", "cron_jobs", "job_definitions", "version_policies"} {
		if err := db.Exec(fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, application_id INTEGER)", tbl)).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("CREATE TABLE version_allocations (id INTEGER PRIMARY KEY, policy_id INTEGER)")
	db.Exec("INSERT INTO applications (id, workspace_id) VALUES (1, 1), (2, 1)")
	db.Exec("INSERT INTO version_policies (id, application_id) VALUES (10, 1), (20, 2)")
	db.Exec("INSERT INTO version_allocations (policy_id) VALUES (10), (10), (20)")
	db.Exec("INSERT INTO cron_jobs (application_id) VALUES (1), (2)")
	db.Exec("INSERT INTO jobs (application_id) VALUES (1), (1), (2)")

	if err := NewApplicationRepository(db).Delete(1); err != nil {
		t.Fatal(err)
	}
	for tbl, want := range map[string]int64{"cron_jobs": 1, "jobs": 1, "version_policies": 1, "version_allocations": 1} {
		var n int64
		db.Table(tbl).Count(&n)
		if n != want {
			t.Errorf("%s: %d rows left, want %d (the other app's)", tbl, n, want)
		}
	}
}
